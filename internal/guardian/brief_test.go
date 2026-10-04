package guardian

import "testing"

func TestBriefInputNormalizesUnknownAnomalyType(t *testing.T) {
	input := briefInputForOverview(Overview{
		Totals: OverviewTotals{
			Waybills:  3,
			Anomalies: 1,
		},
		AnomalyDistribution: []AnomalyDistributionItem{{
			Type:  "ROUTE-IDENTITY-LEAK",
			Count: 1,
		}},
	})

	if input.TopAnomalyType != "other" {
		t.Fatalf("top anomaly type = %q, want other", input.TopAnomalyType)
	}
}
