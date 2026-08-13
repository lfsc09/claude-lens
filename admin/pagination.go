package admin

import "strconv"

const defaultPageSize = 50

var pageSizes = []int{25, 50, 100, 200}

// normalizePageSize validates a "size" query param against the fixed set of
// choices offered by the page-size <select>, falling back to
// defaultPageSize for anything absent, non-numeric, or not in the list —
// mirrors normalizeDashboardRange's handling of an untrusted "range" param.
func normalizePageSize(raw string) int {
	if n, err := strconv.Atoi(raw); err == nil {
		for _, s := range pageSizes {
			if s == n {
				return n
			}
		}
	}
	return defaultPageSize
}

// paginate resolves a requested page number against a result set of `total`
// items and the given pageSize, returning the clamped page, the total page
// count, and the 1-indexed [from, to] range of items shown on that page (to
// == 0 when total == 0). Clamping (rather than erroring) means a stale
// bookmarked page, or one typed past the last page, degrades gracefully to
// the nearest valid page instead of rendering empty or issuing a negative
// SQL OFFSET.
func paginate(requestedPage, pageSize, total int) (page, totalPages, from, to int) {
	totalPages = (total + pageSize - 1) / pageSize
	if totalPages < 1 {
		totalPages = 1
	}

	page = requestedPage
	if page < 1 {
		page = 1
	}
	if page > totalPages {
		page = totalPages
	}

	if total == 0 {
		return page, totalPages, 0, 0
	}
	from = (page-1)*pageSize + 1
	to = from + pageSize - 1
	if to > total {
		to = total
	}
	return page, totalPages, from, to
}
