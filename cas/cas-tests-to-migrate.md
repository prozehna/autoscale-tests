# Cluster Autoscaler (CAS) tests in openshift-tests-private

Inventory of autoscaler-related tests in this repo. "CAS" as a suite name maps to exactly
one file; autoscaler tests overall are spread across four areas.

## 1. The CAS suite proper — `test/extended/clusterinfrastructure/autoscaler.go`

`g.Describe("[sig-cluster-lifecycle] Cluster_Infrastructure CAS")` — 16 tests, all `Author:prozehna`:

| Line | OCP case | Title |
|---|---|---|
| `autoscaler.go:63` | 43174 | ClusterAutoscaler CR could be deleted with foreground deletion |
| `:83` | 45430 | MachineSet scaling from 0 evaluated correctly for new/changed instance types |
| `:126` | 44816 | CVO could remove unrecognized volume mounts |
| `:157` | 47656 | Scale down based on scale down utilization threshold |
| `:230` | 53080 | Add verbosity option to autoscaler CRD |
| `:268` | 44051 | `ClusterAutoscalerUnableToScaleCPULimitReached` alert |
| `:305` | 44211 | `ClusterAutoscalerUnableToScaleMemoryLimitReached` alert |
| `:342` | 37854 | Scale down nodegroup with Failed machine when maxNodeProvisionTime reached |
| `:409` | 28876 | Machineset annotations to support scale from/to zero |
| `:450` | 22038 | Support scale machineset from/to 0 |
| `:488` | 66157 | CAO injects unique labels on Nutanix |
| `:526` | 64869 | Predict correct machineset to scale for a particular arch |
| `:599` | 73113 | CAO upstream scale-from-zero annotations |
| `:632` | 73120 | least-waste expander |
| `:716` | 73446 | priority expander |
| `:793` | 68627 | Rescale up from 0 after first scale up, taint nodes directly |

Helpers live in `test/extended/clusterinfrastructure/autoscaler_utils.go`
(clusterAutoscaler / machineAutoscaler template structs, create/delete).

Related but outside the CAS Describe: `misc_cases.go:94` (OCP-60147) checks the
`cluster-autoscaler` ClusterOperator is marked optional.

## 2. HyperShift CAS — `test/extended/hypershift/hypershift_autoscaling.go`

13 tests, all `Author:lgao`, tagged `AutoScaling`. Note **every one is `g.FIt`** (focused) —
worth knowing before running that package.

- 82716 / 83127 — cluster-autoscaler flags with / without scale down
- 83145 — priority expander
- 82718 — expose CAO's BalancingIgnoredLabels
- 87515 — scale-from-zero feature + API validations
- 87516, 87517, 87518, 87519 — scale-from-zero variants, each in Replace and InPlace flavors (8 tests)

Plus elsewhere in that package:

- `hypershift.go:117` — OCP-43272/43829, autoscaler via hostedCluster autoScaling settings
- `hypershift.go:807` — OCP-49436, NodePool conditions
- `hypershift_install.go:3100` — OCP-83580, ignore `topology.k8s.aws/zone-id` for balancing similar nodegroups
- `hostedclusters.go` — shared helpers (`checkAutoscalingFromZeroEnabled`, nodepool autoscaling patch/restore)

## 3. Hive MachinePool autoscale (Hive's own, not cluster-autoscaler)

- `hive_aws.go:5157` — OCP-28867/41776
- `hive_azure.go:440` — OCP-52415
- `hive_gcp.go:845` — OCP-52411

## 4. Other autoscalers (different components, easy to confuse with CAS)

- `winc/winc.go:271` — OCP-42047, cluster autoscaling with Windows nodes
- `winc/winc.go:2797` and `winc/79100.go` — OCP-79100 HPA with Windows containers (duplicated across both files)
- `node/node.go` — Custom Metrics Autoscaler (KEDA) and Vertical Pod Autoscaler, plus HPA cases
- `mco/machineset.go:279` — reads the `capacity.cluster-autoscaler.kubernetes.io/labels` annotation, not a CAS test

## Running

To run just the CAS suite: `--ginkgo.focus="Cluster_Infrastructure CAS"`.
