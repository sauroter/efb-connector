"""Regression tests for the pure-Python helpers in garmin_fetch.py.

The garminconnect import is now deferred to functions that need it
(see `_import_garmin`), so this test file can be run without that
dependency installed.

Run from the project root:

    .venv/bin/python -m unittest scripts.garmin_fetch_test

or simply:

    make test-python
"""

import contextlib
import io
import json
import tempfile
import unittest
from unittest import mock

from scripts import garmin_fetch as gf


def _activity(name="", parent=17, type_key="other"):
    """Build a minimal Garmin activity payload for filter tests."""
    return {
        "activityName": name,
        "activityType": {"typeKey": type_key, "parentTypeId": parent},
    }


class IsWaterSportTest(unittest.TestCase):
    """Strict filter: parent_type_id == 228 OR legacy typeKey."""

    def test_water_sports_parent_id_matches(self):
        self.assertTrue(gf.is_water_sport(_activity(parent=228, type_key="kayaking_v2")))

    def test_legacy_typekey_matches(self):
        for key in gf.LEGACY_WATER_SPORT_TYPES:
            with self.subTest(typeKey=key):
                self.assertTrue(gf.is_water_sport(_activity(parent=999, type_key=key)))

    def test_non_water_sport_rejected(self):
        for key in ("cycling", "running", "walking", "other", "lap_swimming"):
            with self.subTest(typeKey=key):
                self.assertFalse(gf.is_water_sport(_activity(parent=17, type_key=key)))


class NameMatchesWaterSportTest(unittest.TestCase):
    """Opt-in name fallback, gated on parent_type_id == 17."""

    # ── Names that SHOULD match (parent=17) ─────────────────────────

    def test_user_216_münster_kajak(self):
        # The original feedback that motivated this whole feature.
        self.assertTrue(gf.name_matches_water_sport(_activity("Münster Kajak")))

    def test_german_compounds(self):
        # The regression the second-round review caught: \b in regex
        # used to reject these because the keyword sits inside a
        # German compound noun without a separator.
        for name in (
            "Doppelpaddel-Training",
            "Seekajak",
            "Wildwasserkajak",
            "Bootspaddel-Session",
            "Drachenbootrudern",
            "Wanderkanu",
            "Stechpaddel-Tour",
        ):
            with self.subTest(name=name):
                self.assertTrue(gf.name_matches_water_sport(_activity(name)))

    def test_sup_variants(self):
        for name in (
            "SUP",
            "SUP-Workshop",
            "Stand Up Paddling",
            "Stand-up paddling",
            "Standup Paddle session",
        ):
            with self.subTest(name=name):
                self.assertTrue(gf.name_matches_water_sport(_activity(name)))

    def test_english_water_sports(self):
        for name in (
            "Morning Canoe",
            "Rowing club",
            "Kayak training",
            "Mid Paddel Workout",
            "Rudern auf der Ems",
        ):
            with self.subTest(name=name):
                self.assertTrue(gf.name_matches_water_sport(_activity(name)))

    # ── Names that should NOT match (false-positive guard) ──────────

    def test_support_supper_not_matched(self):
        # \bsup\b specifically prevents these — substring 'sup' appears
        # at word-start in both but trailing \b fails because 'p' is
        # followed by another word character.
        for name in ("Customer support call", "Supper at lake", "Supplies run"):
            with self.subTest(name=name):
                self.assertFalse(gf.name_matches_water_sport(_activity(name)))

    def test_unrelated_activities_not_matched(self):
        for name in ("Sunday Walk", "Cycling tour", "Yoga", "Pilates class"):
            with self.subTest(name=name):
                self.assertFalse(gf.name_matches_water_sport(_activity(name)))

    def test_empty_name_not_matched(self):
        self.assertFalse(gf.name_matches_water_sport(_activity("")))

    # ── Parent-id guard ─────────────────────────────────────────────

    def test_water_sport_name_with_specific_non_fitness_parent_rejected(self):
        # User records a "Münster Kajak" but the activity ended up
        # under a specific non-fitness parent (e.g. cycling=2). The
        # guard must reject — name match alone isn't enough.
        self.assertFalse(gf.name_matches_water_sport(_activity("Münster Kajak", parent=2)))

    def test_water_sport_name_with_water_sport_parent_rejected_here(self):
        # parent=228 is already covered by is_water_sport — the name
        # fallback should not double-count it.
        self.assertFalse(gf.name_matches_water_sport(_activity("kayaking", parent=228)))


class ListActivitiesFilteringTest(unittest.TestCase):
    """End-to-end filtering through list_activities with a fake client."""

    class _FakeClient:
        def __init__(self, activities):
            self._activities = activities

        def get_activities_by_date(self, _start, _end):
            return self._activities

    def _make_activities(self):
        return [
            # Strict water-sport — accepted by default.
            {
                "activityId": 1,
                "activityName": "Real Kajak",
                "activityType": {"typeKey": "kayaking_v2", "parentTypeId": 228},
                "startTimeLocal": "2026-05-30 14:00:00",
            },
            # Mis-tagged "Other" with water-sport name — accepted only
            # via match_by_name fallback.
            {
                "activityId": 2,
                "activityName": "Münster Kajak",
                "activityType": {"typeKey": "other", "parentTypeId": 17},
                "startTimeLocal": "2026-05-30 17:08:52",
            },
            # Unrelated activity.
            {
                "activityId": 3,
                "activityName": "Münster Radfahren",
                "activityType": {"typeKey": "cycling", "parentTypeId": 17},
                "startTimeLocal": "2026-05-30 10:00:00",
            },
        ]

    def test_strict_mode_keeps_only_water_sport(self):
        out = gf.list_activities(self._FakeClient(self._make_activities()), days=30)
        self.assertEqual([a["id"] for a in out], [1])

    def test_match_by_name_recovers_mistagged(self):
        out = gf.list_activities(
            self._FakeClient(self._make_activities()),
            days=30,
            match_by_name=True,
        )
        self.assertEqual([a["id"] for a in out], [1, 2])

    def test_include_all_returns_everything(self):
        out = gf.list_activities(
            self._FakeClient(self._make_activities()),
            days=30,
            include_all=True,
        )
        self.assertEqual([a["id"] for a in out], [1, 2, 3])


class _FakeAuthError(Exception):
    """Stand-in for garminconnect.GarminConnectAuthenticationError."""


class ConnectGarminProfileToleranceTest(unittest.TestCase):
    """connect_garmin must not let non-essential post-auth metadata fetches
    (social profile, user settings) abort the run. Token auth has already
    succeeded by the time those fire, and efb-connector uses none of that
    metadata to list activities. Genuine auth failures must still abort."""

    def _run_connect(self, login_side_effect):
        """Drive connect_garmin with a fake client whose login() raises
        login_side_effect (an exception instance) or succeeds (None).
        Returns (result_or_None, fake_client)."""
        fake_client = mock.Mock(name="GarminClient")
        fake_client.login.side_effect = login_side_effect
        fake_garmin = mock.Mock(return_value=fake_client)
        with tempfile.TemporaryDirectory() as tokenstore:
            with mock.patch.object(
                gf, "_import_garmin", return_value=(fake_garmin, _FakeAuthError)
            ), mock.patch.object(
                gf,
                "get_credentials_from_stdin",
                return_value=("e@example.com", "pw", tokenstore),
            ):
                return gf.connect_garmin({}), fake_client

    def test_tolerates_social_profile_failure(self):
        result, client = self._run_connect(
            _FakeAuthError("Failed to retrieve social profile")
        )
        self.assertIs(result, client)

    def test_tolerates_invalid_profile_data(self):
        result, client = self._run_connect(
            _FakeAuthError("Invalid profile data found")
        )
        self.assertIs(result, client)

    def test_tolerates_user_settings_failure(self):
        result, client = self._run_connect(
            _FakeAuthError("Failed to retrieve user settings")
        )
        self.assertIs(result, client)

    def test_reraises_genuine_auth_failure(self):
        with self.assertRaises(SystemExit) as cm:
            self._run_connect(
                _FakeAuthError("Authentication failed (401 Unauthorized)")
            )
        self.assertEqual(cm.exception.code, 1)

    def test_non_auth_exception_propagates_despite_tolerated_message(self):
        # Tolerance is gated on the exception *type* (auth_error_cls), not the
        # message. A rate-limit error is a sibling class, not an auth error, so
        # even a message that would otherwise match must still abort. This locks
        # in that GarminConnectTooManyRequestsError can never be swallowed.
        class _FakeRateLimitError(Exception):
            pass

        with self.assertRaises(SystemExit) as cm:
            self._run_connect(_FakeRateLimitError("social profile (429)"))
        self.assertEqual(cm.exception.code, 1)

    def test_successful_login_returns_client(self):
        result, client = self._run_connect(None)
        self.assertIs(result, client)


class ValidateCredentialsProfileToleranceTest(unittest.TestCase):
    """validate_credentials must agree with what sync needs: a non-essential
    profile/settings fetch failure is still a valid credential (matching the
    MFA validation path, which never raises for these)."""

    def _run_validate(self, login_side_effect):
        """Drive validate_credentials with a fake client. Returns
        (exit_code, parsed_stdout_json)."""
        fake_client = mock.Mock(name="GarminClient")
        fake_client.login.side_effect = login_side_effect
        fake_garmin = mock.Mock(return_value=fake_client)
        out = io.StringIO()
        with tempfile.TemporaryDirectory() as tokenstore:
            with mock.patch.object(
                gf, "_import_garmin", return_value=(fake_garmin, _FakeAuthError)
            ), mock.patch.object(
                gf,
                "get_credentials_from_stdin",
                return_value=("e@example.com", "pw", tokenstore),
            ), contextlib.redirect_stdout(out):
                with self.assertRaises(SystemExit) as cm:
                    gf.validate_credentials({})
        code = cm.exception.code
        payload = json.loads(out.getvalue()) if out.getvalue().strip() else {}
        return code, payload

    def test_social_profile_failure_is_valid(self):
        code, payload = self._run_validate(
            _FakeAuthError("Failed to retrieve social profile")
        )
        self.assertEqual(code, 0)
        self.assertEqual(payload.get("status"), "ok")

    def test_genuine_auth_failure_is_invalid(self):
        code, _ = self._run_validate(
            _FakeAuthError("Authentication failed (401 Unauthorized)")
        )
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()


class ProfileErrorStatusTest(unittest.TestCase):
    """_profile_error_status recovers the HTTP status garminconnect buried in
    the __cause__ chain, so tolerance can distinguish a dead token (401/403)
    from a transient blip on an endpoint we never read (429/5xx)."""

    def test_status_from_direct_message(self):
        self.assertEqual(
            gf._profile_error_status(Exception("API Error 429 - slow down")), 429
        )

    def test_status_from_cause_chain(self):
        try:
            try:
                raise Exception("API Error 500 - upstream boom")
            except Exception as inner:
                raise _FakeAuthError("Failed to retrieve social profile") from inner
        except _FakeAuthError as outer:
            self.assertEqual(gf._profile_error_status(outer), 500)

    def test_no_status_recoverable(self):
        self.assertIsNone(
            gf._profile_error_status(_FakeAuthError("Invalid profile data found"))
        )

    def test_cyclic_cause_chain_terminates(self):
        a = Exception("no status here")
        b = Exception("nor here")
        a.__cause__ = b
        b.__cause__ = a
        self.assertIsNone(gf._profile_error_status(a))


class InstallProfileToleranceTest(unittest.TestCase):
    """garminconnect >= 0.3.5 treats *any* auth error out of
    _load_profile_and_settings() as 'cached token rejected' and burns a full
    SSO credential login. For a flaky profile endpoint that is a pure waste of
    a good token -- and an escalation when the trigger was itself a 429."""

    def _client_with_profile_error(self, exc):
        client = mock.Mock(name="GarminClient")
        client._load_profile_and_settings = mock.Mock(side_effect=exc)
        gf._install_profile_tolerance(client, _FakeAuthError)
        return client

    @staticmethod
    def _profile_error(message, status=None):
        if status is None:
            return _FakeAuthError(message)
        try:
            try:
                raise Exception(f"API Error {status}")
            except Exception as inner:
                raise _FakeAuthError(message) from inner
        except _FakeAuthError as outer:
            return outer

    def test_transient_429_is_swallowed(self):
        client = self._client_with_profile_error(
            self._profile_error("Failed to retrieve social profile", 429)
        )
        client._load_profile_and_settings()  # must not raise -> no re-login

    def test_transient_500_is_swallowed(self):
        client = self._client_with_profile_error(
            self._profile_error("Failed to retrieve user settings", 500)
        )
        client._load_profile_and_settings()

    def test_causeless_invalid_data_propagates(self):
        # Upstream raises this with no __cause__ when the endpoint answers with
        # a non-dict three times -- what a session the API no longer accepts
        # looks like. Swallowing it would defeat the poisoned-cache recovery.
        client = self._client_with_profile_error(
            self._profile_error("Invalid profile data found")
        )
        with self.assertRaises(_FakeAuthError):
            client._load_profile_and_settings()

    def test_statusless_transport_error_is_swallowed(self):
        # A connection reset carries a __cause__ but no "API Error NNN": still
        # a blip on an endpoint we never read, so the token must survive it.
        try:
            try:
                raise OSError("connection reset by peer")
            except OSError as inner:
                raise _FakeAuthError("Failed to retrieve social profile") from inner
        except _FakeAuthError as exc:
            client = self._client_with_profile_error(exc)
        client._load_profile_and_settings()

    def test_other_4xx_propagates(self):
        client = self._client_with_profile_error(
            self._profile_error("Failed to retrieve social profile", 400)
        )
        with self.assertRaises(_FakeAuthError):
            client._load_profile_and_settings()

    def test_401_still_propagates_so_login_can_reauthenticate(self):
        # A genuinely dead token must reach garminconnect's self-heal; this is
        # the poisoned-cache recovery we deliberately keep.
        client = self._client_with_profile_error(
            self._profile_error("Failed to retrieve social profile", 401)
        )
        with self.assertRaises(_FakeAuthError):
            client._load_profile_and_settings()

    def test_403_still_propagates(self):
        client = self._client_with_profile_error(
            self._profile_error("Failed to retrieve user settings", 403)
        )
        with self.assertRaises(_FakeAuthError):
            client._load_profile_and_settings()

    def test_unrelated_auth_error_still_propagates(self):
        client = self._client_with_profile_error(
            _FakeAuthError("Authentication failed (401 Unauthorized)")
        )
        with self.assertRaises(_FakeAuthError):
            client._load_profile_and_settings()

    def test_success_passes_through(self):
        client = mock.Mock(name="GarminClient")
        client._load_profile_and_settings = mock.Mock(return_value=None)
        gf._install_profile_tolerance(client, _FakeAuthError)
        client._load_profile_and_settings()

    def test_missing_helper_is_a_noop(self):
        # garminconnect < 0.3.5 fetched the profile inline; nothing to wrap.
        class _Bare:
            pass

        client = _Bare()
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            gf._install_profile_tolerance(client, _FakeAuthError)  # must not raise
        self.assertFalse(hasattr(client, "_load_profile_and_settings"))
        # Failing open must not be silent: the shim shadows a *private* method.
        self.assertIn("no _load_profile_and_settings hook", stderr.getvalue())


class ProfileWarningStderrSafetyTest(unittest.TestCase):
    """classifyError() in internal/garmin/python.go lowercases the whole stderr
    blob and tests for "429" *before* its authentication branch. A bare status
    number in the tolerated-failure warning would therefore make a later genuine
    auth failure surface as "Garmin temporarily unavailable" instead of
    prompting the user to re-enter credentials."""

    # Mirrors the substrings classifyError checks before ErrGarminAuth.
    _GO_UNAVAILABLE_KEYWORDS = ("429", "rate limit", "too many",
                                "strategies exhausted", "strategies rate limited")

    def _warning_for(self, status):
        try:
            try:
                raise Exception(f"API Error {status}")
            except Exception as inner:
                raise _FakeAuthError("Failed to retrieve social profile") from inner
        except _FakeAuthError as exc:
            client = mock.Mock(name="GarminClient")
            client._load_profile_and_settings = mock.Mock(side_effect=exc)
            gf._install_profile_tolerance(client, _FakeAuthError)
            stderr = io.StringIO()
            with contextlib.redirect_stderr(stderr):
                client._load_profile_and_settings()
            return stderr.getvalue()

    def test_rate_limited_warning_has_no_go_keyword(self):
        warning = self._warning_for(429)
        self.assertIn("rate-limited", warning)
        for keyword in self._GO_UNAVAILABLE_KEYWORDS:
            self.assertNotIn(keyword, warning.lower(),
                             f"warning would be misclassified by Go on {keyword!r}")

    def test_server_error_warning_has_no_go_keyword(self):
        warning = self._warning_for(503)
        self.assertIn("server-error", warning)
        for keyword in self._GO_UNAVAILABLE_KEYWORDS:
            self.assertNotIn(keyword, warning.lower())


class ValidateMFAProfileToleranceTest(unittest.TestCase):
    """The MFA setup path must tolerate a flaky profile endpoint too.

    garminconnect 0.3.3's resume_login() logged and continued when the profile
    fetch failed; 0.3.11 raises. Unprotected, a user with a correct MFA code
    would authenticate successfully, hit a 429 on the profile fetch, and have
    the exception skip client.client.dump() -- discarding freshly issued tokens
    so Garmin setup could never complete."""

    def test_resume_login_profile_failure_still_dumps_tokens(self):
        dumped = []

        def failing_profile():
            try:
                raise Exception("API Error 429")
            except Exception as inner:
                raise _FakeAuthError("Failed to retrieve social profile") from inner

        fake_client = mock.Mock(name="Garmin")
        fake_client.login.return_value = ("needs_mfa", None)
        fake_client._load_profile_and_settings = mock.Mock(side_effect=failing_profile)
        # resume_login mirrors garminconnect >=0.3.5: fetch profile, let it raise.
        fake_client.resume_login.side_effect = (
            lambda *_: fake_client._load_profile_and_settings()
        )
        fake_client.client.dump.side_effect = lambda path: dumped.append(path)

        with tempfile.TemporaryDirectory() as tokenstore:
            # validate_mfa reads stdin directly: credentials, then the MFA code.
            stdin = io.StringIO(
                json.dumps({"email": "e@example.com", "password": "pw",
                            "tokenstore": tokenstore})
                + "\n" + json.dumps({"mfa_code": "123456"}) + "\n"
            )
            with mock.patch.object(gf, "_import_garmin",
                                   return_value=(mock.Mock(return_value=fake_client),
                                                 _FakeAuthError)), \
                    mock.patch.object(gf.sys, "stdin", stdin), \
                    contextlib.redirect_stdout(io.StringIO()), \
                    contextlib.redirect_stderr(io.StringIO()):
                with self.assertRaises(SystemExit) as cm:
                    gf.validate_mfa()

        self.assertEqual(cm.exception.code, 0, "MFA setup must succeed")
        self.assertTrue(dumped, "tokens must be persisted despite the profile blip")
