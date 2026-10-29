# 679 — Looking ahead: CAS migration gap analysis

Diff of `cas-tests-to-migrate.md` (inventory of autoscaler tests in
`openshift-tests-private`) against what is actually in this repo.

Short answer: **about a third of the CAS suite proper is here (sometimes under
different names), none of sections 2–3, and section 4's non-CAS autoscalers
exist as their own, larger suites.**

The repo's CAS tests all live in one file, `cas/pkg/machineapi/autoscaler.go`
(9 specs under `Describe("Autoscaler should")`), wired in via
`cas/pkg/e2e_test.go`.

## Section 1 — the CAS suite (16 tests)

**Present, same behavior, different name:**

| md entry | Here |
|---|---|
| 22038 scale machineset from/to 0 | `autoscaler.go:243` "It scales from/to zero" |
| 28876 machineset annotations for scale from/to zero | same spec, `:278` asserts `CpuKeyDeprecated` / `MemoryKeyDeprecated` before the MachineAutoscaler exists |
| 73113 CAO upstream scale-from-zero annotations | same spec, `:318` asserts `annotationsutil.CpuKey` / `MemoryKey` after scale-up |
| 64869 predict correct machineset for an arch | `:350` "It scales from/to zero a machine set with the architecture requested by the workload" |
| 73446 priority expander | `:1073` "high priority machineset should be scaled up first" — carries the explicit `// OCP-73446` comment |

**Partially covered:**

- **43174 foreground deletion** — no dedicated spec, but every `AfterEach`
  deletes the ClusterAutoscaler with `DeletePropagationForeground`
  (`:125`) and polls for `NotFound`, so a regression would fail the suite. The
  behavior is exercised, just not named.
- **68627 rescale from 0 after first scale up / taints** — `:460` "cleanup
  deletion information after scale down" asserts machines lose deletion
  annotations and nodes lose `DeletionCandidateOfClusterAutoscaler` /
  `ToBeDeletedByClusterAutoscaler` after scale-in. That's the taint-cleanup
  half; it never re-scales up afterward to prove the group is reusable.
- **47656 scale-down utilization threshold** — scale-down itself is configured
  and asserted everywhere (`ScaleDownConfig` with `UnneededTime`/delays,
  `:74`), but `UtilizationThreshold` is never set or varied, so the threshold
  knob itself is untested.
- **53080 verbosity option** — `LogVerbosity: 4` is set on the shared CR
  (`:84`), so the field is accepted, but nothing asserts it reaches the
  cluster-autoscaler deployment args.

**Absent entirely:** 45430 (new/changed instance types), 44816 (CVO
unrecognized volume mounts), 44051 + 44211 (the two `UnableToScale*LimitReached`
alerts — no alert or Prometheus assertions anywhere in the repo), 37854
(`maxNodeProvisionTime` with a Failed machine), 66157 (CAO unique labels on
Nutanix — "Nutanix" only appears in platform `switch` allow-lists), 73120
(least-waste expander — only `PriorityExpander` is used), and 60147
(`cluster-autoscaler` ClusterOperator marked optional).

**Also worth knowing — this repo has 5 CAS specs that aren't in the md at
all**, apparently from the cluster-autoscaler-operator upstream e2e suite
rather than openshift-tests-private:

- `:592` "should not enforce minimum size" and `:1392` "should enforce minimum
  size and scale up from 0 to 1" (`EnforceNodeGroupMinSize`, on/off pair)
- `:687` "places nodes evenly across node groups" (balance-similar-nodes +
  `BalancingIgnoredLabels` — note this overlaps hypershift's 82718/83580
  conceptually)
- `:912` "scales up and down while respecting MaxNodesTotal"
- `:1201` "should not schedule pods on nodes" (`NewPodScaleUpDelay`)

## Sections 2 and 3 — nothing

No HyperShift CAS and no Hive MachinePool coverage. `hypershift`,
`HostedCluster`, and `Hive` appear nowhere in the repo outside the md itself
(and LICENSE). All 13 hypershift autoscaling tests plus 43272/43829, 49436,
83580 and the three Hive cases are unmigrated.

## Section 4 — mixed

- **Windows (winc 42047, 79100)**: absent.
- **CMA/KEDA, VPA, HPA**: these do exist, but as standalone suites rather than
  migrated cases — `test/e2e/cma` (~17 specs incl. cron/CPU/memory scalers,
  scale-to-zero, pause, validation), `test/e2e/vpa` (~15 specs across
  recommender/admission/updater), `test/e2e/hpa` (~14 specs across pod vs
  container resource, CPU vs memory). Plus `test/e2e/cro` for Cluster Resource
  Override, which the md doesn't mention. Same components as `node.go`,
  considerably broader, no case-number traceability back to the originals.
- **`mco/machineset.go` capacity labels annotation**: the key
  `capacity.cluster-autoscaler.kubernetes.io/labels` is read in
  `cas/pkg/framework/machinesets.go:50` as an arch-inference fallback — a
  helper, not a test, which matches the md's note that it isn't a CAS test.

## Caveat

Only 73446 is explicitly annotated with its OCP number in code, so the other
correspondences above are a reading of what each spec asserts, not something
recorded in the repo. If the migration should be auditable, adding
`// OCP-xxxxx` comments to the five confirmed ones would be cheap.
