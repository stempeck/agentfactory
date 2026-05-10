## BEGIN AgentFactory Agents

Dispatch work to a specialist agent:
```
af sling --agent <name> "task description"
```

| Agent | Type | Description |
|-------|------|-------------|
| `design` | autonomous | Structured design exploration via parallel specialized analysts. |
| `design-v3` | autonomous | Structured design exploration with constraint verification, dependency mapping, and cross-dimension conflict detection for ag... |
| `factoryworker` | autonomous | Full agent work lifecycle from assignment through PR submission. |
| `gherkin-breakdown` | autonomous | Create Gherkin scenarios from an English requirements document through iterative drafting, review, and validation until the f... |
| `investigate` | autonomous | Investigate a codebase question or bug with structured analysis |
| `manager` | interactive | Interactive agent for human-supervised work |
| `mergepatrol` | autonomous | PR merge processor patrol loop. |
| `supervisor` | autonomous | Autonomous agent for independent task execution |
## END AgentFactory Agents
