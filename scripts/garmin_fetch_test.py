"""Regression tests for the pure-Python helpers in garmin_fetch.py.

The garminconnect import is now deferred to functions that need it
(see `_import_garmin`), so this test file can be run without that
dependency installed.

Run from the project root:

    .venv/bin/python -m unittest scripts.garmin_fetch_test

or simply:

    make test-python
"""

import unittest

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


if __name__ == "__main__":
    unittest.main()
