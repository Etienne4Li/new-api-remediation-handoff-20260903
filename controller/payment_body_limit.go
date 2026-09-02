package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// Payment/webhook payloads are compact signed JSON documents. Keep a separate
// limit from the much larger general relay request limit so a caller cannot
// force a payment handler to allocate tens or hundreds of megabytes before
// signature/JSON validation runs. The route middleware applies an anonymous
// limit too, but handlers retain this defense when invoked directly or through
// a differently configured router.
const maxPaymentRequestBodyBytes int64 = 1 << 20

func readPaymentRequestBody(c *gin.Context) ([]byte, error) {
	if c == nil || c.Request == nil {
		return nil, common.ErrRequestBodyTooLarge
	}
	return common.ReadBodyLimited(c.Request.Body, c.Request.ContentLength, maxPaymentRequestBodyBytes)
}
