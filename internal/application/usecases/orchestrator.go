package usecases

import (
    "context"
    "time"

    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/infrastructure/metrics"
)

// ApplyUseCase delegates to ConfigureNetworkUseCase.applyConfiguration
type ApplyUseCase struct{ parent *ConfigureNetworkUseCase }

func (a *ApplyUseCase) Apply(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
    return a.parent.applyConfiguration(ctx, iface, name)
}

// ValidateUseCase delegates to ConfigureNetworkUseCase.validateConfiguration
type ValidateUseCase struct{ parent *ConfigureNetworkUseCase }

func (v *ValidateUseCase) Validate(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
    return v.parent.validateConfiguration(ctx, iface, name)
}

// ProcessingUseCase composes apply + validate and updates status
type ProcessingUseCase struct{ parent *ConfigureNetworkUseCase; applier *ApplyUseCase; validator *ValidateUseCase }

func (p *ProcessingUseCase) Process(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
    start := time.Now()
    // apply
    if err := p.applier.Apply(ctx, iface, name); err != nil {
        metrics.RecordInterfaceProcessing(name.String(), "failed", time.Since(start).Seconds())
        return err
    }
    // validate
    if err := p.validator.Validate(ctx, iface, name); err != nil {
        metrics.RecordInterfaceProcessing(name.String(), "failed", time.Since(start).Seconds())
        return err
    }
    // update status
    if err := p.parent.repository.UpdateInterfaceStatus(ctx, iface.ID(), entities.StatusConfigured); err != nil {
        metrics.RecordInterfaceProcessing(name.String(), "failed", time.Since(start).Seconds())
        return err
    }
    metrics.RecordInterfaceProcessing(name.String(), "success", time.Since(start).Seconds())
    return nil
}

