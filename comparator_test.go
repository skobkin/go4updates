package updates

import "testing"

func TestSemVerComparator(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    Status
	}{
		{"plain bump", "1.2.3", "1.2.4", StatusUpdateAvailable},
		{"major bump", "1.9.9", "2.0.0", StatusUpdateAvailable},
		{"current without v, latest with v", "1.2.3", "v1.3.0", StatusUpdateAvailable},
		{"current with v, latest without v", "v1.2.3", "1.3.0", StatusUpdateAvailable},
		{"both with v", "v1.2.3", "v1.3.0", StatusUpdateAvailable},
		{"up to date", "1.3.0", "1.2.3", StatusUpToDate},
		{"equal versions", "1.2.3", "1.2.3", StatusUpToDate},
		{"equal with mixed v", "v1.2.3", "1.2.3", StatusUpToDate},
		{"build metadata ignored on equal", "1.2.3+build.5", "1.2.3+other", StatusUpToDate},
		{"build metadata ignored on bump", "1.2.3+build.5", "1.2.4", StatusUpdateAvailable},
		{"prerelease older than release", "1.2.3-rc.1", "1.2.3", StatusUpdateAvailable},
		{"release newer than prerelease", "1.2.3", "1.2.3-rc.1", StatusUpToDate},
		{"prerelease ordering", "1.2.3-alpha", "1.2.3-beta", StatusUpdateAvailable},
		{"whitespace trimmed", " 1.2.3 ", "1.2.4", StatusUpdateAvailable},
		{"empty current", "", "1.2.3", StatusUnknown},
		{"empty latest", "1.2.3", "", StatusUnknown},
		{"both empty", "", "", StatusUnknown},
		{"invalid current", "not-a-version", "1.2.3", StatusUnknown},
		{"invalid latest", "1.2.3", "release-42", StatusUnknown},
		{"incomplete current treated as patch zero", "1.2", "1.2.3", StatusUpdateAvailable},
		{"incomplete latest treated as patch zero", "1.2.3", "1.2", StatusUpToDate},
		{"major only treated as minor and patch zero", "2", "1.9.9", StatusUpToDate},
		{"uppercase V is not accepted", "V1.2.3", "1.2.4", StatusUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SemVerComparator{}.Compare(tt.current, tt.latest)
			if got != tt.want {
				t.Errorf("Compare(%q, %q) = %v, want %v", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestComparatorFuncImplementsComparator(t *testing.T) {
	var comparator Comparator = ComparatorFunc(func(current, latest string) Status {
		if current == latest {
			return StatusUpToDate
		}

		return StatusUpdateAvailable
	})

	if got := comparator.Compare("1.0.0", "1.0.0"); got != StatusUpToDate {
		t.Errorf("ComparatorFunc.Compare() = %v, want %v", got, StatusUpToDate)
	}

	if got := comparator.Compare("1.0.0", "2.0.0"); got != StatusUpdateAvailable {
		t.Errorf("ComparatorFunc.Compare() = %v, want %v", got, StatusUpdateAvailable)
	}
}

func TestStatusString(t *testing.T) {
	tests := map[Status]string{
		StatusUnknown:         "unknown",
		StatusUpToDate:        "up_to_date",
		StatusUpdateAvailable: "update_available",
		Status(99):            "unknown",
	}

	for status, want := range tests {
		if got := status.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", uint8(status), got, want)
		}
	}
}
