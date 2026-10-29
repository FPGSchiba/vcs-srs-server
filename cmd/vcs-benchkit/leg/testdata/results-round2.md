### Leg 2 one-big-net (arm S)

**Result:** knee 1038  
**Calibration:** receive capacity ≥ 51150 pps (lower bound: reflector dropped 186000 destinations)  
**N_max:** 4000 · **predicted knee:** 1400 · **starts:** 2

| kind | # | N | joined | outcome | loss | server CPU | gen CPU | gen health | join failures | hello tries max | server drops | control lost | ramp joins/s | reasons |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| calibration | - | 1024 | 1024 | delivered 100% | - | - | - | ok | 0 | 1 | - | - | - |  |
| calibration | - | 2048 | 2048 | delivered 0% | - | - | - | ok | 0 | 2 | - | - | - |  |
| sweep | 1 | 1038 | 1038 | pass | 0.00% | 39% | 19% | ok (warn: gc_pause_p99) | 0 | 2 | 0 | 0 | - |  |
| sweep | 1 | 1038 | 1038 | control_lost | 0.00% | 39% | 19% | ok (warn: gc_pause_p99) | 0 | 2 | 0 | 2 | 9.6 |  |

#### Join bursts

| talker | from N | added | joined | failures | joins/s | join p99 | hello retried | result |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| live | 668 | 150 | 120 | 30 | 9.6 | 7.40s | 30 | join failures |

_Shared GitHub runner (4 vCPU), loopback, relative only. Not a capacity figure._
