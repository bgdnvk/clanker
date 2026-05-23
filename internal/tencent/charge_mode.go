package tencent

import "strings"

// Tencent uses two billing-mode conventions across its services:
//
//   - String form (CVM, CBS, Lighthouse, CLB, Postgres): values like "PREPAID"
//     / "POSTPAID_BY_HOUR" / "SPOTPAID" / "CDHPAID" (CVM/CBS uppercase) or
//     "prepaid" / "postpaid" (Postgres lowercase). All non-prepaid strings map
//     to "POSTPAID".
//   - Integer form, with NO consistent mapping across services:
//     CDB     PayType:     0=prepaid, 1=postpaid    (inverted vs the others)
//     Redis   BillingMode: 1=prepaid, 0=postpaid
//     MongoDB PayMode:     1=prepaid, 0=postpaid
//     CynosDB PayMode:     1=prepaid, 0=postpaid
//     WAF     PayMode:     1=prepaid, 0=postpaid
//
// The helpers below normalize all of these to two canonical strings
// "PREPAID" / "POSTPAID" so the JSON output is uniform across resource types.
// An empty string is returned when the input is nil — the field is then
// omitted from JSON via `omitempty` so callers can tell "we don't know" apart
// from "POSTPAID".

const (
	billingPrepaid  = "PREPAID"
	billingPostpaid = "POSTPAID"
)

func normChargeTypeStr(s *string) string {
	if s == nil {
		return ""
	}
	if strings.EqualFold(*s, "PREPAID") {
		return billingPrepaid
	}
	return billingPostpaid
}

// CDB inverts the convention: 0 means prepaid, 1 means postpaid.
func normPayTypeCDB(v *int64) string {
	if v == nil {
		return ""
	}
	if *v == 0 {
		return billingPrepaid
	}
	return billingPostpaid
}

// Redis / CynosDB: 1=prepaid, 0=postpaid (int64).
func normBillingModeInt64(v *int64) string {
	if v == nil {
		return ""
	}
	if *v == 1 {
		return billingPrepaid
	}
	return billingPostpaid
}

// MongoDB / WAF: 1=prepaid, 0=postpaid (uint64).
func normBillingModeUint64(v *uint64) string {
	if v == nil {
		return ""
	}
	if *v == 1 {
		return billingPrepaid
	}
	return billingPostpaid
}
