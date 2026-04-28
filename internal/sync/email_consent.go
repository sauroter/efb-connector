package sync

const efbConsentURL = "https://efb.kanu-efb.de/interpretation/usersmap"

// efbConsentEmail returns a (subject, htmlBody) pair informing the user
// that EFB v2026.1 added a one-time track-usage consent step. The action
// happens on the EFB portal — there's nothing to do on our side once
// they consent — so the body links directly to the EFB tracks page.
func efbConsentEmail(lang string) (subject, body string) {
	if lang == "de" {
		return "EFB Connector: Bitte Track-Nutzungsvereinbarung bestätigen",
			`<p>Hallo,</p>
<p>EFB hat in Version 2026.1 eine einmalige Zustimmung zur anonymisierten Verwendung deiner Tracks eingeführt. Solange diese Zustimmung fehlt, werden Uploads vom EFB-Portal stillschweigend abgelehnt — die Synchronisation läuft, kommt aber nicht durch.</p>
<p>Bitte öffne dazu deine Track-Übersicht auf eFB und klicke einmalig auf <b>"ich stimme zu"</b>:</p>
<p><a href="` + efbConsentURL + `" target="_blank">Meine Tracks auf eFB öffnen</a></p>
<p>Danach läuft die nächste geplante Synchronisation automatisch wieder durch — du musst hier nichts weiter tun.</p>
<p>Viele Grüße,<br>EFB Connector</p>`
	}

	return "EFB Connector: Please accept the track-usage agreement",
		`<p>Hi,</p>
<p>EFB version 2026.1 introduced a one-time consent step for the anonymised use of your uploaded tracks. While that consent is missing, the EFB portal silently rejects every upload — sync runs, but no track ever lands.</p>
<p>Please open your tracks page on eFB and click <b>"ich stimme zu"</b> once:</p>
<p><a href="` + efbConsentURL + `" target="_blank">Open Meine Tracks on eFB</a></p>
<p>The next scheduled sync will resume automatically afterwards — there's nothing to do on our side.</p>
<p>Best,<br>EFB Connector</p>`
}
