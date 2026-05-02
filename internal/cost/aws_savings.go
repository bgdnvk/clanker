package cost

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer"
	"github.com/aws/aws-sdk-go-v2/service/costexplorer/types"
)

// GetSavingsRecommendations fetches AWS Savings Plan + Reserved Instance
// purchase recommendations from Cost Explorer. Both are pulled in one
// call (different SDK methods) and merged into a single report so the
// CLI can show "here is the highest-savings commitment, regardless of
// shape" instead of forcing the operator to switch tabs.
//
// Cost Explorer returns recommendations only for accounts with usage
// history; brand-new accounts produce an empty list and a Note rather
// than an error.
func (p *AWSProvider) GetSavingsRecommendations(ctx context.Context, lookback, term string) (*SavingsReport, error) {
	lookback = normaliseLookback(lookback)
	term = normaliseTerm(term)

	report := &SavingsReport{
		GeneratedAt: time.Now().UTC(),
		Provider:    "aws",
		Lookback:    lookback,
		Term:        term,
	}

	// 1) Savings Plans — three families: Compute, EC2 Instance, SageMaker.
	for _, family := range []types.SupportedSavingsPlansType{
		types.SupportedSavingsPlansTypeComputeSp,
		types.SupportedSavingsPlansTypeEc2InstanceSp,
		types.SupportedSavingsPlansTypeSagemakerSp,
	} {
		recs, err := p.fetchSavingsPlanRecs(ctx, family, lookback, term)
		if err != nil {
			// Don't fail the whole report on one family — Cost Explorer
			// occasionally returns AccessDeniedException for SageMaker
			// when the account never used it. Surface the issue in
			// Notes instead so the operator knows what was skipped.
			appendCostNote(&report.Notes, fmt.Sprintf("%s recommendations skipped: %v", family, err))
			continue
		}
		report.Recommendations = append(report.Recommendations, recs...)
	}

	// 2) Reserved Instances — services that have RI offerings on
	// Cost Explorer. Each call hits one service.
	//
	// AWS Cost Explorer's GetReservationPurchaseRecommendation expects
	// the *full* service name as it appears in the AWS billing console,
	// not the short SDK identifier ("AmazonEC2"). Passing the short
	// form returns ValidationException with a helpful list of the
	// 8 supported full names — those are exactly what's enumerated
	// here. Adding MemoryDB + DynamoDB to surface RI recs that were
	// previously silently dropped.
	for _, svc := range []string{
		"Amazon Elastic Compute Cloud - Compute",
		"Amazon Relational Database Service",
		"Amazon ElastiCache",
		"Amazon OpenSearch Service",
		"Amazon Redshift",
		"Amazon MemoryDB Service",
		"Amazon DynamoDB Service",
	} {
		recs, err := p.fetchRIRecs(ctx, svc, lookback, term)
		if err != nil {
			appendCostNote(&report.Notes, fmt.Sprintf("%s RI recommendations skipped: %v", riServiceLabel(svc), err))
			continue
		}
		report.Recommendations = append(report.Recommendations, recs...)
	}

	// Aggregate + sort.
	for _, r := range report.Recommendations {
		report.TotalEstimatedSavings += r.EstimatedSavings
	}
	sortSavingsRecsByEstimatedSavings(report.Recommendations)

	if len(report.Recommendations) == 0 && report.Notes == "" {
		report.Notes = "no commitment recommendations — account may not have enough usage history"
	}
	return report, nil
}

func (p *AWSProvider) fetchSavingsPlanRecs(ctx context.Context, family types.SupportedSavingsPlansType, lookback, term string) ([]SavingsRecommendation, error) {
	out, err := p.client.GetSavingsPlansPurchaseRecommendation(ctx, &costexplorer.GetSavingsPlansPurchaseRecommendationInput{
		SavingsPlansType:     family,
		TermInYears:          types.TermInYears(term),
		PaymentOption:        types.PaymentOptionNoUpfront,
		LookbackPeriodInDays: types.LookbackPeriodInDays(lookback),
		PageSize:             50,
	})
	if err != nil {
		return nil, err
	}
	if out.SavingsPlansPurchaseRecommendation == nil {
		return nil, nil
	}

	familyLabel := savingsPlanFamilyLabel(family)
	recs := make([]SavingsRecommendation, 0, len(out.SavingsPlansPurchaseRecommendation.SavingsPlansPurchaseRecommendationDetails))
	for _, d := range out.SavingsPlansPurchaseRecommendation.SavingsPlansPurchaseRecommendationDetails {
		monthlySavings := parseFloatString(d.EstimatedMonthlySavingsAmount)
		percent := parseFloatString(d.EstimatedSavingsPercentage)
		hourlyCommit := parseFloatString(d.HourlyCommitmentToPurchase)
		upfront := parseFloatString(d.UpfrontCost)

		recs = append(recs, SavingsRecommendation{
			Provider:           "aws",
			Kind:               SavingsKindSavingsPlan,
			Family:             familyLabel,
			Term:               term,
			PaymentOption:      string(types.PaymentOptionNoUpfront),
			UpfrontCost:        upfront,
			HourlyCommitment:   hourlyCommit,
			EstimatedSavings:   monthlySavings,
			EstimatedSavingsPc: percent,
			BreakevenMonths:    breakevenMonths(upfront, monthlySavings),
			Detail:             savingsPlanDetail(d),
		})
	}
	return recs, nil
}

func (p *AWSProvider) fetchRIRecs(ctx context.Context, service, lookback, term string) ([]SavingsRecommendation, error) {
	out, err := p.client.GetReservationPurchaseRecommendation(ctx, &costexplorer.GetReservationPurchaseRecommendationInput{
		Service:              aws.String(service),
		TermInYears:          types.TermInYears(term),
		PaymentOption:        types.PaymentOptionNoUpfront,
		LookbackPeriodInDays: types.LookbackPeriodInDays(lookback),
		PageSize:             50,
	})
	if err != nil {
		return nil, err
	}
	if len(out.Recommendations) == 0 {
		return nil, nil
	}

	var recs []SavingsRecommendation
	for _, rec := range out.Recommendations {
		for _, d := range rec.RecommendationDetails {
			monthlySavings := parseFloatString(d.EstimatedMonthlySavingsAmount)
			percent := parseFloatString(d.EstimatedMonthlySavingsPercentage)
			upfront := parseFloatString(d.UpfrontCost)

			recs = append(recs, SavingsRecommendation{
				Provider:           "aws",
				Kind:               SavingsKindReservedInstance,
				Service:            riServiceLabel(service),
				Family:             instanceFamilyFromDetails(d.InstanceDetails),
				Term:               term,
				PaymentOption:      string(types.PaymentOptionNoUpfront),
				UpfrontCost:        upfront,
				EstimatedSavings:   monthlySavings,
				EstimatedSavingsPc: percent,
				BreakevenMonths:    breakevenMonths(upfront, monthlySavings),
				Detail:             riDetail(d),
			})
		}
	}
	return recs, nil
}

func parseFloatString(s *string) float64 {
	if s == nil {
		return 0
	}
	v, err := strconv.ParseFloat(strings.TrimSpace(*s), 64)
	if err != nil {
		return 0
	}
	return v
}

func breakevenMonths(upfront, monthlySavings float64) float64 {
	if monthlySavings <= 0 {
		return 0
	}
	return upfront / monthlySavings
}

func sortSavingsRecsByEstimatedSavings(recs []SavingsRecommendation) {
	// Insertion sort — fine for the small list sizes (<100) Cost Explorer
	// returns. Avoids an import cycle with sort+ pulled into types.go.
	for i := 1; i < len(recs); i++ {
		for j := i; j > 0 && recs[j-1].EstimatedSavings < recs[j].EstimatedSavings; j-- {
			recs[j-1], recs[j] = recs[j], recs[j-1]
		}
	}
}

func normaliseLookback(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "SEVEN_DAYS", "7":
		return "SEVEN_DAYS"
	case "THIRTY_DAYS", "30":
		return "THIRTY_DAYS"
	case "SIXTY_DAYS", "60", "":
		return "SIXTY_DAYS"
	}
	return "SIXTY_DAYS"
}

func normaliseTerm(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "THREE_YEARS", "3":
		return "THREE_YEARS"
	case "ONE_YEAR", "1", "":
		return "ONE_YEAR"
	}
	return "ONE_YEAR"
}

func savingsPlanFamilyLabel(t types.SupportedSavingsPlansType) string {
	switch t {
	case types.SupportedSavingsPlansTypeComputeSp:
		return "Compute"
	case types.SupportedSavingsPlansTypeEc2InstanceSp:
		return "EC2 Instance"
	case types.SupportedSavingsPlansTypeSagemakerSp:
		return "SageMaker"
	}
	return string(t)
}

func savingsPlanDetail(d types.SavingsPlansPurchaseRecommendationDetail) string {
	parts := []string{}
	if d.SavingsPlansDetails != nil {
		if v := d.SavingsPlansDetails.InstanceFamily; v != nil && *v != "" {
			parts = append(parts, "family="+*v)
		}
		if v := d.SavingsPlansDetails.Region; v != nil && *v != "" {
			parts = append(parts, "region="+*v)
		}
	}
	if v := d.CurrentAverageHourlyOnDemandSpend; v != nil && *v != "" {
		parts = append(parts, "current $/hr="+*v)
	}
	return strings.Join(parts, ", ")
}

// riServiceLabel maps the full AWS service name (which Cost Explorer's
// GetReservationPurchaseRecommendation API requires) to the short
// human-readable label rendered in the CLI table and the JSON output's
// `service` field. The legacy short codes ("AmazonEC2") are kept as
// fall-throughs so callers carrying older identifiers still work.
func riServiceLabel(svc string) string {
	switch svc {
	case "Amazon Elastic Compute Cloud - Compute", "AmazonEC2":
		return "EC2"
	case "Amazon Relational Database Service", "AmazonRDS":
		return "RDS"
	case "Amazon ElastiCache", "AmazonElastiCache":
		return "ElastiCache"
	case "Amazon OpenSearch Service", "AmazonOpenSearchService":
		return "OpenSearch"
	case "Amazon Redshift", "AmazonRedshift":
		return "Redshift"
	case "Amazon MemoryDB Service":
		return "MemoryDB"
	case "Amazon DynamoDB Service":
		return "DynamoDB"
	}
	return svc
}

func instanceFamilyFromDetails(d *types.InstanceDetails) string {
	if d == nil {
		return ""
	}
	switch {
	case d.EC2InstanceDetails != nil && d.EC2InstanceDetails.Family != nil:
		return *d.EC2InstanceDetails.Family
	case d.RDSInstanceDetails != nil && d.RDSInstanceDetails.Family != nil:
		return *d.RDSInstanceDetails.Family
	case d.ElastiCacheInstanceDetails != nil && d.ElastiCacheInstanceDetails.Family != nil:
		return *d.ElastiCacheInstanceDetails.Family
	case d.RedshiftInstanceDetails != nil && d.RedshiftInstanceDetails.Family != nil:
		return *d.RedshiftInstanceDetails.Family
	}
	return ""
}

func riDetail(d types.ReservationPurchaseRecommendationDetail) string {
	parts := []string{}
	if v := d.RecommendedNumberOfInstancesToPurchase; v != nil && *v != "" {
		parts = append(parts, "qty="+*v)
	}
	if d.InstanceDetails != nil && d.InstanceDetails.EC2InstanceDetails != nil {
		ec2 := d.InstanceDetails.EC2InstanceDetails
		if ec2.InstanceType != nil && *ec2.InstanceType != "" {
			parts = append(parts, "instance="+*ec2.InstanceType)
		}
		if ec2.Region != nil && *ec2.Region != "" {
			parts = append(parts, "region="+*ec2.Region)
		}
	}
	return strings.Join(parts, ", ")
}

// appendCostNote concatenates a non-empty note onto an accumulator. Local
// to this package so we don't depend on the sre helper.
func appendCostNote(dst *string, note string) {
	if note == "" {
		return
	}
	if *dst == "" {
		*dst = note
		return
	}
	*dst += "; " + note
}
