package tencent

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	cam "github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/cam/v20190116"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common"
	"github.com/tencentcloud/tencentcloud-sdk-go/tencentcloud/common/profile"
)

// listCAMUsers prints every CAM sub-account user. CAM is account-global so
// region is irrelevant. The SDK version we ship does not expose MFA flags
// from ListUsers/GetUser, so the hygiene audit operates on what is visible
// (presence/absence of console_login, phone, email).
func listCAMUsers(c *Client) error {
	client, err := newCAMClient(c)
	if err != nil {
		return fmt.Errorf("init cam client: %w", err)
	}
	resp, err := client.ListUsers(cam.NewListUsersRequest())
	if err != nil {
		return fmt.Errorf("ListUsers: %w", friendlyError(err))
	}

	fmt.Println("Tencent Cloud CAM Sub-Account Users:")
	fmt.Println()
	if resp == nil || resp.Response == nil || len(resp.Response.Data) == 0 {
		fmt.Println("  No sub-account users found")
		return nil
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "UID\tNAME\tNICKNAME\tEMAIL\tCONSOLE_LOGIN\tPHONE_SET\tCREATED")
	for _, u := range resp.Response.Data {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%v\t%v\t%s\n",
			derefUint64(u.Uid),
			derefString(u.Name),
			derefString(u.NickName),
			derefString(u.Email),
			derefUint64(u.ConsoleLogin) == 1,
			strings.TrimSpace(derefString(u.PhoneNum)) != "" && derefString(u.PhoneNum) != "-",
			derefString(u.CreateTime),
		)
	}
	return tw.Flush()
}

func newCAMClient(c *Client) (*cam.Client, error) {
	cred := common.NewCredential(c.creds.SecretID, c.creds.SecretKey)
	cpf := profile.NewClientProfile()
	cpf.HttpProfile.Endpoint = "cam.tencentcloudapi.com"
	return cam.NewClient(cred, "ap-guangzhou", cpf)
}
