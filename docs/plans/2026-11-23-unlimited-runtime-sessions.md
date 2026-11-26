# Unlimited Runtime Sessions Plan

**Goal:** Make launch sessions unlimited by default while preserving optional caller-selected expiration.

**Architecture:** Treat `seconds == 0` as unlimited at the MCP/core boundary, Go session manager, and native observer. Positive durations retain the 60-second minimum and lose the 1800-second maximum. Update the bundled GUI call and tool contract so they no longer silently choose a timeout.

**Verification:** Add failing tests for omitted/zero/long durations, run Go tests and a Windows observer lifecycle test, then rebuild the observer and release package.

---

1. Add tests proving omitted and zero seconds map to unlimited, durations above 1800 seconds are accepted, and invalid short positive durations remain rejected.
2. Implement optional duration validation and disable the Go timer when seconds is zero.
3. Update the native observer to wait until an explicit duration or a stop/host event.
4. Update the MCP schema, GUI launch default, and usage documentation.
5. Run relevant tests, rebuild the native observer, and produce a fresh release package.
