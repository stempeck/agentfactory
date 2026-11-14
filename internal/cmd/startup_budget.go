package cmd

import "github.com/stempeck/agentfactory/internal/memory"

// The per-mode sums are literals rather than expressions so that raising one ceiling has to
// restate the total it moves; TestStartupBudgetConstantsArePinned holds the two together.
const (
	startupRoleClaudeMdBytes      = 16384
	startupPlainPrimeBytes        = 16384
	startupUsingAgentfactoryBytes = 65536
	startupMemoryInjectionBytes   = memory.DefaultTotalBytes + memoryInjectFrameBytes
	startupMailInjectionBytes     = mailInjectTotalBytes + mailInjectFrameBytes
	startupMemoryListBytes        = 8192
	startupAgentsMdBytes          = 16384
	startupPrimeHookBytes         = 1024

	startupSumCompactionBytes    = 133248
	startupSumNudgeBytes         = 137856
	startupSumInitialPromptBytes = 137856

	startupTurnBudgetTokens = 90000
)
