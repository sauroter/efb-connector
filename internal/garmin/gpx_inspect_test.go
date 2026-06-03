package garmin

import "testing"

func TestHasTrackPoints(t *testing.T) {
	// stubGPX mirrors the 658-byte payload Garmin returns for an
	// activity recorded without GPS — captured from prod for the
	// failing activity 21618651195 (user 219) on 2026-06-02.
	const stubGPX = `<?xml version="1.0" encoding="UTF-8"?>
<gpx creator="Garmin Connect" version="1.1"
  xsi:schemaLocation="http://www.topografix.com/GPX/1/1 http://www.topografix.com/GPX/11.xsd"
  xmlns:ns3="http://www.garmin.com/xmlschemas/TrackPointExtension/v1"
  xmlns="http://www.topografix.com/GPX/1/1"
  xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:ns2="http://www.garmin.com/xmlschemas/GpxExtensions/v3">
  <metadata>
    <link href="connect.garmin.com">
      <text>Garmin Connect</text>
    </link>
    <time>2026-01-21T13:12:16.000Z</time>
  </metadata>
  <trk>
    <name>Kajakfahren</name>
    <type>kayaking_v2</type>
    <trkseg/>
  </trk>
</gpx>`

	const realGPX = `<?xml version="1.0" encoding="UTF-8"?>
<gpx version="1.1"><trk><trkseg>
  <trkpt lat="53.55" lon="9.99"><ele>1.2</ele><time>2026-05-01T10:00:00Z</time></trkpt>
  <trkpt lat="53.56" lon="9.98"><ele>1.3</ele><time>2026-05-01T10:00:05Z</time></trkpt>
</trkseg></trk></gpx>`

	tests := []struct {
		name string
		gpx  string
		want bool
	}{
		{"empty trkseg from Garmin stub", stubGPX, false},
		{"self-closing trkseg only", `<gpx><trk><trkseg/></trk></gpx>`, false},
		{"open/close trkseg with no points", `<gpx><trk><trkseg></trkseg></trk></gpx>`, false},
		{"single trkpt", `<gpx><trk><trkseg><trkpt lat="0" lon="0"/></trkseg></trk></gpx>`, true},
		{"realistic two-point track", realGPX, true},
		{"empty input", "", false},
		{"only whitespace", "   \n\t  ", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasTrackPoints([]byte(tt.gpx)); got != tt.want {
				t.Errorf("HasTrackPoints() = %v, want %v", got, tt.want)
			}
		})
	}
}
