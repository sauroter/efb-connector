package garmin

import "bytes"

// trackPointMarker is the opening fragment of a GPX <trkpt> element.
// Garmin Connect always emits <trkpt lat="..." lon="..."> (with attributes),
// so a `<trkpt ` or `<trkpt>` fragment is sufficient to detect that the
// file has at least one recorded point.
var trackPointMarker = []byte("<trkpt")

// HasTrackPoints reports whether the GPX payload contains at least one
// <trkpt> element. Garmin occasionally returns a structurally-valid GPX
// shell with an empty <trkseg/> when an activity was recorded without
// GPS (indoors, GPS disabled, manual entry). EFB rejects these with a
// misleading "XML-Fehler" alert; detecting the empty case before upload
// lets the engine skip them cleanly.
//
// Byte-level scan rather than XML parse: Garmin's output never contains
// the marker inside comments or CDATA, and the cost of a real parser
// on every sync would dwarf the cost of the upload it saves.
func HasTrackPoints(gpxData []byte) bool {
	return bytes.Contains(gpxData, trackPointMarker)
}
