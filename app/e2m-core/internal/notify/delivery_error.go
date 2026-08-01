package notify

import (
	"errors"
	"net"
	"strings"
)

type DeliveryError struct {
	Code      string
	Message   string
	Retryable bool
}

func (e *DeliveryError) Error() string { return e.Message }

func Permanent(code, message string) error {
	return &DeliveryError{Code: code, Message: message}
}

func Retryable(code, message string) error {
	return &DeliveryError{Code: code, Message: message, Retryable: true}
}

func Classify(err error) error {
	if err == nil {
		return nil
	}
	var deliveryErr *DeliveryError
	if errors.As(err, &deliveryErr) {
		return deliveryErr
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return Retryable("network_error", "网络暂时不可用")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "http 429") || strings.Contains(message, "http 5") || strings.Contains(message, "request failed") {
		return Retryable("provider_unavailable", "接收渠道暂时不可用")
	}
	if strings.Contains(message, "invalid target") || strings.Contains(message, "not public") {
		return Permanent("invalid_target", "Webhook 地址不符合安全要求")
	}
	return Permanent("delivery_rejected", "接收渠道拒绝了消息")
}

func SafeDeliveryError(err error) (code, message string, retryable bool) {
	classified := Classify(err)
	var deliveryErr *DeliveryError
	if errors.As(classified, &deliveryErr) {
		return deliveryErr.Code, deliveryErr.Message, deliveryErr.Retryable
	}
	return "delivery_failed", "消息发送失败", false
}
