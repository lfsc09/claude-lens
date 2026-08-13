package admin

import "testing"

func TestNormalizePageSize(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"25", 25},
		{"50", 50},
		{"100", 100},
		{"200", 200},
		{"", defaultPageSize},
		{"0", defaultPageSize},
		{"-50", defaultPageSize},
		{"75", defaultPageSize},
		{"abc", defaultPageSize},
	}
	for _, tt := range tests {
		if got := normalizePageSize(tt.in); got != tt.want {
			t.Errorf("normalizePageSize(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestPaginate(t *testing.T) {
	tests := []struct {
		name                     string
		requestedPage, pageSize, total int
		wantPage, wantTotalPages, wantFrom, wantTo int
	}{
		{"first page, exact multiple", 1, 50, 150, 1, 3, 1, 50},
		{"middle page", 2, 50, 150, 2, 3, 51, 100},
		{"last page, partial", 3, 50, 150, 3, 3, 101, 150},
		{"no results", 1, 50, 0, 1, 1, 0, 0},
		{"single partial page", 1, 50, 12, 1, 1, 1, 12},
		{"requested page beyond last clamps down", 99, 50, 150, 3, 3, 101, 150},
		{"requested page below 1 clamps up", 0, 50, 150, 1, 3, 1, 50},
		{"negative requested page clamps up", -5, 50, 150, 1, 3, 1, 50},
		{"total not evenly divisible by page size", 1, 25, 26, 1, 2, 1, 25},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			page, totalPages, from, to := paginate(tt.requestedPage, tt.pageSize, tt.total)
			if page != tt.wantPage || totalPages != tt.wantTotalPages || from != tt.wantFrom || to != tt.wantTo {
				t.Errorf("paginate(%d, %d, %d) = (%d, %d, %d, %d), want (%d, %d, %d, %d)",
					tt.requestedPage, tt.pageSize, tt.total,
					page, totalPages, from, to,
					tt.wantPage, tt.wantTotalPages, tt.wantFrom, tt.wantTo)
			}
		})
	}
}
