# Inverter monitoring module — design notes

Start with **[HANDOFF.md](HANDOFF.md)**.

First message to give Claude Code:

> Read docs/inverter/HANDOFF.md and the files it links. Then read this repository's
> CLAUDE.md, AI_CONTEXT.md and README architecture section, and study how an existing
> collector, its settings, alerts, history and page are wired. Propose a plan for the
> inverter module (where it plugs in, the producer/model definition format, which
> existing files change) before writing any code.

The original vendor manuals (Top One Power Modbus protocol, E30, ZLAN7144N2) are not
included here because of their copyright notices; keep them locally and attach them when
needed.
