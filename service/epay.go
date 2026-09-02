package service

import (
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func GetCallbackAddress() string {
	if callbackAddress := operation_setting.GetPaymentRuntimeConfig().CustomCallbackAddress; callbackAddress != "" {
		return callbackAddress
	}
	{
		return system_setting.GetServerAddress()
	}
}
