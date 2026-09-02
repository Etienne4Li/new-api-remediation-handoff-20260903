package common

import (
	"strconv"

	"github.com/gin-gonic/gin"
)

type PageInfo struct {
	Page     int `json:"page"`      // page num 页码
	PageSize int `json:"page_size"` // page size 页大小

	Total int `json:"total"` // 总条数，后设置
	Items any `json:"items"` // 数据，后设置
}

// MaxPageSize bounds every list query that uses the shared page parser.  A
// non-positive LIMIT has special meaning to GORM (it disables the limit), so
// callers must never receive a zero or negative page size.
const MaxPageSize = 100

func (p *PageInfo) GetStartIdx() int {
	if p == nil || p.Page <= 1 || p.PageSize <= 0 {
		return 0
	}
	page := p.Page - 1
	maxInt := int(^uint(0) >> 1)
	if page > maxInt/p.PageSize {
		return maxInt
	}
	return page * p.PageSize
}

func (p *PageInfo) GetEndIdx() int {
	if p == nil || p.Page <= 0 || p.PageSize <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if p.Page > maxInt/p.PageSize {
		return maxInt
	}
	return p.Page * p.PageSize
}

func (p *PageInfo) GetPageSize() int {
	return p.PageSize
}

func (p *PageInfo) GetPage() int {
	return p.Page
}

func (p *PageInfo) SetTotal(total int) {
	p.Total = total
}

func (p *PageInfo) SetItems(items any) {
	p.Items = items
}

func GetPageQuery(c *gin.Context) *PageInfo {
	pageInfo := &PageInfo{Page: 1, PageSize: ItemsPerPage}
	if page, err := strconv.Atoi(c.Query("p")); err == nil && page > 0 {
		pageInfo.Page = page
	}

	// Keep the historical aliases, but accept only positive values.  In
	// particular, page_size=-1 must not reach GORM as Limit(-1), which means
	// "remove the LIMIT" for several dialects.
	for _, key := range []string{"page_size", "ps", "size"} {
		if pageSize, err := strconv.Atoi(c.Query(key)); err == nil && pageSize > 0 {
			pageInfo.PageSize = pageSize
			break
		}
	}
	if pageInfo.PageSize <= 0 {
		pageInfo.PageSize = 1
	}
	if pageInfo.PageSize > MaxPageSize {
		pageInfo.PageSize = MaxPageSize
	}
	return pageInfo
}
