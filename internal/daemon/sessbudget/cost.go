package sessbudget

import (
	"math"
	"sort"

	"github.com/aloysbr/pigtail/pkg/pigtail"
)

// gigabyte is the unit of the cost report's memory columns: 2^30 bytes, the
// GiB that « Go » means everywhere else in pigtail's output.
const gigabyte = 1 << 30

type typeKey struct{ repo, status string }

type typeAcc struct {
	sessions map[string]bool
	rows     int
	cpu      float64
	mem      float64 // bytes, summed
	peak     uint64
}

// Aggregate prices the session types over a window: a type is a repo and a
// status (busy, idle or waiting, as sampled — a native shell was recorded as
// idle). Per type: the number of distinct sessions, the mean CPU percent and
// the mean and peak footprint over the rows of the window. Sorted by mean
// memory, heaviest first; the figures are rounded for display (CPU to the
// integer, memory to a tenth of a Go) after the sort.
func Aggregate(window string, samples []Sample) pigtail.SessionCost {
	accs := map[typeKey]*typeAcc{}
	for _, smp := range samples {
		for _, r := range smp.Sessions {
			k := typeKey{r.Repo, r.Status}
			a := accs[k]
			if a == nil {
				a = &typeAcc{sessions: map[string]bool{}}
				accs[k] = a
			}
			a.sessions[r.SessionID] = true
			a.rows++
			a.cpu += r.CPUPercent
			a.mem += float64(r.FootprintBytes)
			if r.FootprintBytes > a.peak {
				a.peak = r.FootprintBytes
			}
		}
	}
	type rec struct {
		k    typeKey
		a    *typeAcc
		mean float64
	}
	recs := make([]rec, 0, len(accs))
	for k, a := range accs {
		recs = append(recs, rec{k, a, a.mem / float64(a.rows)})
	}
	sort.Slice(recs, func(i, j int) bool {
		if recs[i].mean != recs[j].mean {
			return recs[i].mean > recs[j].mean
		}
		if recs[i].k.repo != recs[j].k.repo {
			return recs[i].k.repo < recs[j].k.repo
		}
		return recs[i].k.status < recs[j].k.status
	})
	out := pigtail.SessionCost{Window: window, Samples: len(samples), Types: make([]pigtail.SessionCostType, 0, len(recs))}
	for _, r := range recs {
		out.Types = append(out.Types, pigtail.SessionCostType{
			Repo: r.k.repo, Status: r.k.status, Sessions: len(r.a.sessions),
			CPUAvgPct:    math.Round(r.a.cpu / float64(r.a.rows)),
			MemoryAvgGB:  round1(r.mean / gigabyte),
			MemoryPeakGB: round1(float64(r.a.peak) / gigabyte),
		})
	}
	return out
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
