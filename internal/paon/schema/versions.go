package schema

const (
	Mastodon4219Version = "20230907150100"
	Mastodon4323Version = "20241007071624"
)

// mastodon43UpgradeVersions is the reviewed set of upstream migration markers
// between the only supported legacy schema and the final Mastodon 4.3.23
// schema. Both the migration runner and every Paon process startup guard use
// this inventory so an unknown partial or fork-specific marker cannot be
// admitted by one path and rejected by the other.
var mastodon43UpgradeVersions = map[string]struct{}{
	"20231006183200": {}, "20231018192110": {}, "20231018193209": {}, "20231018193355": {}, "20231018193659": {},
	"20231210154528": {}, "20231211234923": {}, "20231212073317": {}, "20231222100226": {},
	"20240109103012": {}, "20240111033014": {},
	"20240217171534": {}, "20240221195424": {}, "20240221195828": {}, "20240221211359": {}, "20240222193403": {}, "20240222203722": {}, "20240227191620": {},
	"20240304090449": {}, "20240307180905": {}, "20240310123453": {}, "20240312100644": {}, "20240312105620": {}, "20240320140159": {}, "20240320163441": {}, "20240321160706": {}, "20240322125607": {}, "20240322130318": {}, "20240322161611": {},
	"20240510192043": {}, "20240513095755": {}, "20240513123807": {}, "20240522041528": {},
	"20240603195202": {}, "20240607093446": {}, "20240607093954": {}, "20240607094603": {}, "20240607094856": {},
	"20240712064044": {}, "20240713171841": {}, "20240713171909": {}, "20240720140205": {}, "20240724181224": {},
	"20240808114841": {}, "20240808124338": {}, "20240808124339": {}, "20240808125420": {},
	"20240909014637": {}, "20240916190140": {}, "20241007071624": {},
}

func Mastodon43UpgradeVersionKnown(version string) bool {
	_, ok := mastodon43UpgradeVersions[version]
	return ok
}

func Mastodon43UpgradeVersionCount() int {
	return len(mastodon43UpgradeVersions)
}
