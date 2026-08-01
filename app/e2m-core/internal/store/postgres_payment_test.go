package store

import (
	"context"
	"os"
	"testing"

	"e2m.local/contracts"
)

func TestPostgresPaymentConfigAndProviderRoundTrip(t *testing.T) {
	dsn := os.Getenv("E2M_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("E2M_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	st, err := NewPostgresStore(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	config, err := st.UpsertPaymentConfig(ctx, contracts.PaymentConfig{
		EnabledPaymentTypes: []string{"alipay"}, OrderTimeoutMinutes: 30,
		MaxPendingOrders: 3, LoadBalanceStrategy: "round-robin",
	})
	if err != nil || config.UpdatedAt.IsZero() {
		t.Fatalf("config: %+v err=%v", config, err)
	}
	loadedConfig, err := st.GetPaymentConfig(ctx)
	if err != nil || len(loadedConfig.EnabledPaymentTypes) != 1 {
		t.Fatalf("load config: %+v err=%v", loadedConfig, err)
	}

	provider, err := st.CreatePaymentProvider(ctx, contracts.PaymentProvider{
		ProviderKey: contracts.PaymentProviderEasyPay, Name: "pg-" + newID("payment"),
		Config: map[string]string{"pid": "1001"}, SecretRefs: map[string]string{"pkey": "opaque-ref"},
		SupportedTypes: []string{"alipay"}, Limits: map[string]contracts.PaymentMethodLimit{"alipay": {SingleMin: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.DeletePaymentProvider(context.Background(), provider.ID) })
	provider.Enabled = true
	provider.Name += "-updated"
	updated, err := st.UpdatePaymentProvider(ctx, provider)
	if err != nil || !updated.Enabled || updated.SecretRefs["pkey"] != "opaque-ref" {
		t.Fatalf("update: %+v err=%v", updated, err)
	}
	loaded, err := st.GetPaymentProvider(ctx, provider.ID)
	if err != nil || loaded.Limits["alipay"].SingleMin != 1 {
		t.Fatalf("load: %+v err=%v", loaded, err)
	}
}
