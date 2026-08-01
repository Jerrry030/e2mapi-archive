package store

import (
	"context"
	"testing"
	"time"

	"e2m.local/contracts"
)

func TestMemoryPaymentConfigAndProviderUseDeepCopies(t *testing.T) {
	st := NewMemoryStore(time.Now())
	ctx := context.Background()
	config, err := st.UpsertPaymentConfig(ctx, contracts.PaymentConfig{
		EnabledPaymentTypes: []string{"alipay"}, LoadBalanceStrategy: "round-robin",
	})
	if err != nil {
		t.Fatal(err)
	}
	config.EnabledPaymentTypes[0] = "mutated"
	storedConfig, err := st.GetPaymentConfig(ctx)
	if err != nil || storedConfig.EnabledPaymentTypes[0] != "alipay" {
		t.Fatalf("config alias leaked: %+v err=%v", storedConfig, err)
	}

	created, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		ProviderKey: contracts.PaymentProviderEasyPay, Name: "main",
		Config: map[string]string{"pid": "1001"}, SecretRefs: map[string]string{"pkey": "ref"},
		SupportedTypes: []string{"alipay"}, Limits: map[string]contracts.PaymentMethodLimit{"alipay": {SingleMin: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	created.Config["pid"] = "mutated"
	created.SecretRefs["pkey"] = "mutated"
	created.SupportedTypes[0] = "mutated"
	created.Limits["alipay"] = contracts.PaymentMethodLimit{SingleMin: 99}
	stored, err := st.GetPaymentProvider(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Config["pid"] != "1001" || stored.SecretRefs["pkey"] != "ref" || stored.SupportedTypes[0] != "alipay" || stored.Limits["alipay"].SingleMin != 1 {
		t.Fatalf("provider alias leaked: %+v", stored)
	}
}
