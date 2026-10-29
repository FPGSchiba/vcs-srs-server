### Leg 2 one-big-net (arm S)

**Result:** knee 1038  
**Calibration:** receive capacity ≥ 51150 pps (lower bound); stopped at N=4096: bot 3702: no HELLO_ACK after 5 tries  
**N_max:** 4000 · **predicted knee:** 1400 · **starts:** 2

| kind | # | N | joined | outcome | loss | server CPU | gen CPU | gen health | join failures | hello tries max | server drops | control lost | ramp joins/s | reasons |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| calibration | - | 1024 | 1024 | delivered 100% | - | - | - | ok | 0 | 1 | - | - | - |  |
| calibration | - | 2048 | 2048 | delivered 0% | - | - | - | ok | 0 | 2 | - | - | - |  |
| sweep | 1 | 1038 | 1038 | pass | 0.00% | 39% | 19% | ok | 0 | 2 | 0 | 0 | - |  |
| sweep | 2 | 2076 | 2074 | breach | - | - | - | - | 2 | 5 | - | 0 | - | join failure: 2 of 1038 new bots failed (hello 2); hello tries max 5 |

_Shared GitHub runner (4 vCPU), loopback, relative only. Not a capacity figure._
