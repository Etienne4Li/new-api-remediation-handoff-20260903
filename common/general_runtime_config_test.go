package common

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeneralRuntimeConfigPublishesCoherentSnapshots(t *testing.T) {
	previous := GetGeneralRuntimeConfig()
	t.Cleanup(func() {
		UpdateGeneralRuntimeConfig(func(cfg *GeneralRuntimeConfig) { *cfg = previous })
	})

	first := GeneralRuntimeConfig{
		QuotaForNewUser: 100, QuotaForInviter: 200, QuotaForInvitee: 300, QuotaRemindThreshold: 400,
		ChannelDisableThreshold: 5.5, AutomaticDisableChannelEnabled: true, AutomaticEnableChannelEnabled: false,
		DrawingEnabled: true, TaskEnabled: false, DataExportEnabled: true, DataExportInterval: 5,
		DataExportDefaultTime: "hour", TopUpLink: "https://first.example/wallet", Footer: "first", Logo: "first-logo",
		FileUploadPermission: 1, FileDownloadPermission: 2, ImageUploadPermission: 3, ImageDownloadPermission: 4,
		DisplayInCurrencyEnabled: true, DisplayTokenStatEnabled: false, DefaultCollapseSidebar: true,
	}
	second := first
	second.QuotaForNewUser = 1000
	second.QuotaForInviter = 2000
	second.QuotaForInvitee = 3000
	second.QuotaRemindThreshold = 4000
	second.ChannelDisableThreshold = 9.5
	second.AutomaticDisableChannelEnabled = false
	second.AutomaticEnableChannelEnabled = true
	second.DrawingEnabled = false
	second.TaskEnabled = true
	second.DataExportEnabled = false
	second.DataExportInterval = 10
	second.DataExportDefaultTime = "day"
	second.TopUpLink = "https://second.example/wallet"
	second.Footer = "second"
	second.Logo = "second-logo"
	second.FileUploadPermission = 11
	second.FileDownloadPermission = 12
	second.ImageUploadPermission = 13
	second.ImageDownloadPermission = 14
	second.DisplayInCurrencyEnabled = false
	second.DisplayTokenStatEnabled = true
	second.DefaultCollapseSidebar = false

	UpdateGeneralRuntimeConfig(func(cfg *GeneralRuntimeConfig) { *cfg = first })
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	seen := make(chan GeneralRuntimeConfig, 512)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 512; i++ {
			value := first
			if i%2 == 1 {
				value = second
			}
			UpdateGeneralRuntimeConfig(func(cfg *GeneralRuntimeConfig) { *cfg = value })
		}
	}()
	go func() {
		defer wg.Done()
		defer close(seen)
		<-start
		for i := 0; i < 512; i++ {
			seen <- GetGeneralRuntimeConfig()
		}
	}()
	close(start)
	wg.Wait()
	for got := range seen {
		require.Truef(t, generalRuntimeConfigEqual(got, first) || generalRuntimeConfigEqual(got, second), "mixed snapshot: %+v", got)
	}
}

func generalRuntimeConfigEqual(a, b GeneralRuntimeConfig) bool {
	return a == b
}
