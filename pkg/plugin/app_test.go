package plugin

import (
	"context"
	"testing"

	"github.com/grafana/grafana-plugin-sdk-go/backend"
)

func TestAppLifecycleIsIndependentFromFactoryContext(t *testing.T) {
	factoryCtx, cancelFactory := context.WithCancel(context.Background())
	cancelFactory()

	instance, err := NewApp(factoryCtx, backend.AppInstanceSettings{})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	app := instance.(*App)

	select {
	case <-app.rootCtx.Done():
		t.Fatal("app lifecycle must not inherit factory context cancellation")
	default:
	}

	app.Dispose()
	select {
	case <-app.rootCtx.Done():
	default:
		t.Fatal("disposing app must cancel its lifecycle context")
	}
}
