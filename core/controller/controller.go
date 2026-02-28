package controller

import (
	"github.com/uber/tango/core/storage"
	"github.com/uber/tango/orchestrator"
	pb "github.com/uber/tango/tangopb"
	"go.uber.org/fx"
	"go.uber.org/zap"
)

// Params are the parameters for the controller.
type Params struct {
	fx.In
	Logger       *zap.Logger
	Storage      storage.Storage
	Orchestrator orchestrator.Orchestrator
}

type controller struct {
	logger       *zap.Logger
	storage      storage.Storage
	orchestrator orchestrator.Orchestrator
}

// NewController creates a new controller.
func NewController(p Params) pb.TangoYARPCServer {
	return &controller{
		logger:       p.Logger,
		storage:      p.Storage,
		orchestrator: p.Orchestrator,
	}
}
