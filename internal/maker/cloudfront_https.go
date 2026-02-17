package maker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	awsManagedCachePolicyCachingDisabled   = "4135ea2d-6df8-44a3-9df3-4b5a84be39ad"
	awsManagedOriginRequestPolicyAllViewer = "216adef6-5c7f-47e4-b989-5492eafa07d3"
)

func maybeEnsureHTTPSViaCloudFront(ctx context.Context, bindings map[string]string, opts ExecOptions) error {
	if opts.Destroyer {
		return nil
	}
	if strings.TrimSpace(opts.Profile) == "" {
		return nil
	}
	albDNS := strings.TrimSpace(bindings["ALB_DNS"])
	tgARN := strings.TrimSpace(bindings["TG_ARN"])
	instanceID := strings.TrimSpace(bindings["INSTANCE_ID"])
	if albDNS == "" {
		return nil
	}
	// Scope: only when this looks like an app behind an ALB (target group and/or instance present).
	if tgARN == "" && instanceID == "" {
		return nil
	}

	if strings.TrimSpace(bindings["HTTPS_URL"]) != "" {
		return nil
	}
	if strings.TrimSpace(bindings["CLOUDFRONT_DOMAIN"]) != "" {
		bindings["HTTPS_URL"] = "https://" + strings.TrimSpace(bindings["CLOUDFRONT_DOMAIN"])
		return nil
	}

	// Stable key so repeated runs reuse the same distribution.
	key := albDNS
	if tgARN != "" {
		key += "|" + tgARN
	} else {
		key += "|" + instanceID
	}
	if q := strings.TrimSpace(bindings["PLAN_QUESTION"]); q != "" {
		key += "|" + q
	}
	comment := fmt.Sprintf("clanker:https:%s", shortStableHash(key))

	id, domain, status, err := findCloudFrontDistributionByComment(ctx, comment, opts.Profile, opts.Writer)
	if err == nil && id != "" && domain != "" {
		_, _ = fmt.Fprintf(opts.Writer, "[https] found existing CloudFront distribution (id=%s status=%s)\n", id, status)
		_ = waitForCloudFrontDistributionDeployed(ctx, opts, id, opts.Writer)
		bindings["CLOUDFRONT_ID"] = id
		bindings["CLOUDFRONT_DOMAIN"] = domain
		bindings["HTTPS_URL"] = "https://" + domain
		return nil
	}

	_, _ = fmt.Fprintf(opts.Writer, "[https] creating CloudFront distribution for ALB origin %s...\n", albDNS)

	cfg := cloudFrontDistributionConfig{
		CallerReference: comment,
		Comment:         comment,
		Enabled:         true,
		PriceClass:      "PriceClass_100",
		Origins: cloudFrontOrigins{
			Quantity: 1,
			Items: []cloudFrontOrigin{{
				Id:         "alb-origin",
				DomainName: albDNS,
				CustomOriginConfig: &cloudFrontCustomOriginConfig{
					HTTPPort:             80,
					HTTPSPort:            443,
					OriginProtocolPolicy: "http-only",
					OriginSslProtocols: cloudFrontOriginSSLProtocols{
						Quantity: 1,
						Items:    []string{"TLSv1.2"},
					},
				},
			}},
		},
		DefaultCacheBehavior: cloudFrontDefaultCacheBehavior{
			TargetOriginId:       "alb-origin",
			ViewerProtocolPolicy: "redirect-to-https",
			AllowedMethods: cloudFrontAllowedMethods{
				Quantity: 7,
				Items:    []string{"GET", "HEAD", "OPTIONS", "PUT", "POST", "PATCH", "DELETE"},
				CachedMethods: cloudFrontCachedMethods{
					Quantity: 3,
					Items:    []string{"GET", "HEAD", "OPTIONS"},
				},
			},
			CachePolicyId:         awsManagedCachePolicyCachingDisabled,
			OriginRequestPolicyId: awsManagedOriginRequestPolicyAllViewer,
			Compress:              true,
		},
		Restrictions: cloudFrontRestrictions{
			GeoRestriction: cloudFrontGeoRestriction{RestrictionType: "none", Quantity: 0},
		},
		ViewerCertificate: cloudFrontViewerCertificate{CloudFrontDefaultCertificate: true},
	}

	cfgJSON, _ := json.Marshal(cfg)

	createArgs := []string{
		"cloudfront", "create-distribution",
		"--distribution-config", string(cfgJSON),
		"--query", "Distribution.[Id,DomainName,Status]",
		"--output", "text",
		"--profile", opts.Profile,
		"--no-cli-pager",
	}

	out, createErr := runAWSCommandStreaming(ctx, createArgs, nil, io.Discard)
	if createErr != nil {
		// Retry by discovery: eventual consistency or duplicate caller reference.
		id2, domain2, status2, findErr := findCloudFrontDistributionByComment(ctx, comment, opts.Profile, opts.Writer)
		if findErr == nil && id2 != "" && domain2 != "" {
			_, _ = fmt.Fprintf(opts.Writer, "[https] create failed but distribution exists (id=%s status=%s); continuing\n", id2, status2)
			_ = waitForCloudFrontDistributionDeployed(ctx, opts, id2, opts.Writer)
			bindings["CLOUDFRONT_ID"] = id2
			bindings["CLOUDFRONT_DOMAIN"] = domain2
			bindings["HTTPS_URL"] = "https://" + domain2
			return nil
		}
		return fmt.Errorf("cloudfront create-distribution failed: %w", createErr)
	}

	parts := strings.Fields(strings.TrimSpace(out))
	if len(parts) < 2 {
		return fmt.Errorf("cloudfront create-distribution unexpected output: %q", strings.TrimSpace(out))
	}
	id = strings.TrimSpace(parts[0])
	domain = strings.TrimSpace(parts[1])
	status = ""
	if len(parts) >= 3 {
		status = strings.TrimSpace(parts[2])
	}

	_, _ = fmt.Fprintf(opts.Writer, "[https] CloudFront distribution created (id=%s status=%s)\n", id, status)
	_ = waitForCloudFrontDistributionDeployed(ctx, opts, id, opts.Writer)

	bindings["CLOUDFRONT_ID"] = id
	bindings["CLOUDFRONT_DOMAIN"] = domain
	bindings["HTTPS_URL"] = "https://" + domain
	return nil
}

func findCloudFrontDistributionByComment(ctx context.Context, comment, profile string, w io.Writer) (id, domain, status string, err error) {
	comment = strings.TrimSpace(comment)
	profile = strings.TrimSpace(profile)
	if comment == "" || profile == "" {
		return "", "", "", fmt.Errorf("missing comment/profile")
	}

	// Note: CloudFront is global; region flag is ignored.
	args := []string{
		"cloudfront", "list-distributions",
		"--query", fmt.Sprintf("DistributionList.Items[?Comment=='%s'] | [0].[Id,DomainName,Status]", escapeJMES(comment)),
		"--output", "text",
		"--profile", profile,
		"--no-cli-pager",
	}
	out, e := runAWSCommandStreaming(ctx, args, nil, io.Discard)
	if e != nil {
		return "", "", "", e
	}
	out = strings.TrimSpace(out)
	if out == "" || strings.Contains(strings.ToLower(out), "none") {
		return "", "", "", fmt.Errorf("not found")
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		_, _ = fmt.Fprintf(w, "[https] list-distributions returned: %q\n", out)
		return "", "", "", fmt.Errorf("unexpected list-distributions output")
	}
	id = strings.TrimSpace(fields[0])
	domain = strings.TrimSpace(fields[1])
	status = ""
	if len(fields) >= 3 {
		status = strings.TrimSpace(fields[2])
	}
	return id, domain, status, nil
}

func escapeJMES(s string) string {
	// We only embed this inside single quotes in JMESPath.
	return strings.ReplaceAll(s, "'", "\\'")
}

type cloudFrontDistributionConfig struct {
	CallerReference      string                         `json:"CallerReference"`
	Comment              string                         `json:"Comment"`
	Enabled              bool                           `json:"Enabled"`
	PriceClass           string                         `json:"PriceClass,omitempty"`
	Origins              cloudFrontOrigins              `json:"Origins"`
	DefaultCacheBehavior cloudFrontDefaultCacheBehavior `json:"DefaultCacheBehavior"`
	Restrictions         cloudFrontRestrictions         `json:"Restrictions"`
	ViewerCertificate    cloudFrontViewerCertificate    `json:"ViewerCertificate"`
}

type cloudFrontOrigins struct {
	Quantity int                `json:"Quantity"`
	Items    []cloudFrontOrigin `json:"Items"`
}

type cloudFrontOrigin struct {
	Id                 string                        `json:"Id"`
	DomainName         string                        `json:"DomainName"`
	CustomOriginConfig *cloudFrontCustomOriginConfig `json:"CustomOriginConfig,omitempty"`
}

type cloudFrontCustomOriginConfig struct {
	HTTPPort             int                          `json:"HTTPPort"`
	HTTPSPort            int                          `json:"HTTPSPort"`
	OriginProtocolPolicy string                       `json:"OriginProtocolPolicy"`
	OriginSslProtocols   cloudFrontOriginSSLProtocols `json:"OriginSslProtocols"`
}

type cloudFrontOriginSSLProtocols struct {
	Quantity int      `json:"Quantity"`
	Items    []string `json:"Items"`
}

type cloudFrontDefaultCacheBehavior struct {
	TargetOriginId        string                   `json:"TargetOriginId"`
	ViewerProtocolPolicy  string                   `json:"ViewerProtocolPolicy"`
	AllowedMethods        cloudFrontAllowedMethods `json:"AllowedMethods"`
	CachePolicyId         string                   `json:"CachePolicyId,omitempty"`
	OriginRequestPolicyId string                   `json:"OriginRequestPolicyId,omitempty"`
	Compress              bool                     `json:"Compress"`
}

type cloudFrontAllowedMethods struct {
	Quantity      int                     `json:"Quantity"`
	Items         []string                `json:"Items"`
	CachedMethods cloudFrontCachedMethods `json:"CachedMethods"`
}

type cloudFrontCachedMethods struct {
	Quantity int      `json:"Quantity"`
	Items    []string `json:"Items"`
}

type cloudFrontRestrictions struct {
	GeoRestriction cloudFrontGeoRestriction `json:"GeoRestriction"`
}

type cloudFrontGeoRestriction struct {
	RestrictionType string `json:"RestrictionType"`
	Quantity        int    `json:"Quantity"`
}

type cloudFrontViewerCertificate struct {
	CloudFrontDefaultCertificate bool `json:"CloudFrontDefaultCertificate"`
}
