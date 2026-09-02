package model

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newUserFillTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := fmt.Sprintf("file:%s?mode=memory&cache=shared", strings.ReplaceAll(t.Name(), "/", "_"))
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&User{}))
	return db
}

func TestFillUserLookupsPropagateNotFound(t *testing.T) {
	originalDB := DB
	DB = newUserFillTestDB(t)
	t.Cleanup(func() { DB = originalDB })

	tests := []struct {
		name string
		user User
		fill func(*User) error
	}{
		{name: "id", user: User{Id: 999}, fill: func(u *User) error { return u.FillUserById() }},
		{name: "email", user: User{Email: "missing@example.com"}, fill: func(u *User) error { return u.FillUserByEmail() }},
		{name: "github", user: User{GitHubId: "missing-github"}, fill: func(u *User) error { return u.FillUserByGitHubId() }},
		{name: "discord", user: User{DiscordId: "missing-discord"}, fill: func(u *User) error { return u.FillUserByDiscordId() }},
		{name: "oidc", user: User{OidcId: "missing-oidc"}, fill: func(u *User) error { return u.FillUserByOidcId() }},
		{name: "wechat", user: User{WeChatId: "missing-wechat"}, fill: func(u *User) error { return u.FillUserByWeChatId() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.fill(&test.user)
			require.ErrorIs(t, err, gorm.ErrRecordNotFound)
		})
	}

	telegram := User{TelegramId: "missing-telegram"}
	require.EqualError(t, telegram.FillUserByTelegramId(), "该 Telegram 账户未绑定")
}

func TestFillUserLookupsPropagateDatabaseErrors(t *testing.T) {
	originalDB := DB
	db := newUserFillTestDB(t)
	DB = db
	t.Cleanup(func() { DB = originalDB })

	forcedErr := errors.New("forced user lookup failure")
	const callbackName = "test:fill_user_lookup_error"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Trim(tx.Statement.Table, "`\"") == "users" {
			tx.AddError(forcedErr)
		}
	}))
	t.Cleanup(func() { _ = db.Callback().Query().Remove(callbackName) })

	lookups := []struct {
		name string
		user User
		fill func(*User) error
	}{
		{name: "id", user: User{Id: 1}, fill: func(u *User) error { return u.FillUserById() }},
		{name: "email", user: User{Email: "a@example.com"}, fill: func(u *User) error { return u.FillUserByEmail() }},
		{name: "github", user: User{GitHubId: "github"}, fill: func(u *User) error { return u.FillUserByGitHubId() }},
		{name: "discord", user: User{DiscordId: "discord"}, fill: func(u *User) error { return u.FillUserByDiscordId() }},
		{name: "oidc", user: User{OidcId: "oidc"}, fill: func(u *User) error { return u.FillUserByOidcId() }},
		{name: "wechat", user: User{WeChatId: "wechat"}, fill: func(u *User) error { return u.FillUserByWeChatId() }},
		{name: "telegram", user: User{TelegramId: "telegram"}, fill: func(u *User) error { return u.FillUserByTelegramId() }},
	}
	for _, lookup := range lookups {
		t.Run(lookup.name, func(t *testing.T) {
			err := lookup.fill(&lookup.user)
			require.ErrorIs(t, err, forcedErr)
		})
	}
}
