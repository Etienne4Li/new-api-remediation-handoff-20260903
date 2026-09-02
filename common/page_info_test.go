package common

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func pageInfoForQuery(t *testing.T, query string) *PageInfo {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/list?"+query, nil)
	return GetPageQuery(c)
}

func TestGetPageQueryRejectsNonPositiveValues(t *testing.T) {
	previousItemsPerPage := ItemsPerPage
	ItemsPerPage = 25
	t.Cleanup(func() { ItemsPerPage = previousItemsPerPage })

	for _, query := range []string{"page_size=0", "page_size=-1", "page_size=bad", "page_size=-1&ps=-2&size=0"} {
		page := pageInfoForQuery(t, query)
		assert.Equal(t, 1, page.Page, query)
		assert.Equal(t, ItemsPerPage, page.PageSize, query)
	}

	page := pageInfoForQuery(t, "p=0&page_size=20")
	assert.Equal(t, 1, page.Page)
	assert.Equal(t, 20, page.PageSize)
	page = pageInfoForQuery(t, "p=-10&ps=20")
	assert.Equal(t, 1, page.Page)
	assert.Equal(t, 20, page.PageSize)
}

func TestGetPageQuerySupportsAliasesAndCapsPageSize(t *testing.T) {
	assert.Equal(t, 17, pageInfoForQuery(t, "ps=17").PageSize)
	assert.Equal(t, 18, pageInfoForQuery(t, "size=18").PageSize)
	assert.Equal(t, MaxPageSize, pageInfoForQuery(t, "page_size=1000").PageSize)
	// The canonical key wins when both values are valid.
	assert.Equal(t, 19, pageInfoForQuery(t, "page_size=19&ps=20").PageSize)
}

func TestPageInfoOffsetsSaturateInsteadOfOverflowing(t *testing.T) {
	maxInt := int(^uint(0) >> 1)
	page := &PageInfo{Page: maxInt, PageSize: MaxPageSize}
	assert.Equal(t, maxInt, page.GetStartIdx())
	assert.Equal(t, maxInt, page.GetEndIdx())

	assert.Equal(t, 0, (&PageInfo{Page: -1, PageSize: 10}).GetStartIdx())
	assert.Equal(t, 0, (&PageInfo{Page: 1, PageSize: 0}).GetStartIdx())

	// A normal page remains exact, guarding against accidental saturation of
	// in-range offsets.
	page = &PageInfo{Page: 3, PageSize: 7}
	require.Equal(t, 14, page.GetStartIdx())
	require.Equal(t, 21, page.GetEndIdx())
}
