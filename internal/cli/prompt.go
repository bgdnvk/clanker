package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// PromptForInstall asks user permission to install missing tools
// Returns true if the user approves installation
func PromptForInstall(missing []DependencyStatus) (bool, error) {
	if len(missing) == 0 {
		return false, nil
	}

	fmt.Println("\nThe following CLI tools are missing or need to be updated:")
	fmt.Println()

	for _, dep := range missing {
		status := "not installed"
		if dep.Installed && dep.Message != "" {
			status = dep.Message
		}
		required := ""
		if dep.Required {
			required = " (required)"
		}
		fmt.Printf("  - %s: %s%s\n", dep.Name, status, required)
	}

	fmt.Println()
	fmt.Println("Clanker can install these tools automatically.")
	fmt.Println("Installation may require sudo privileges.")
	fmt.Println()

	return promptYesNo("Do you want to install the missing tools?")
}

// PromptForSingleInstall asks permission to install a single tool
func PromptForSingleInstall(dep DependencyStatus) (bool, error) {
	status := "not installed"
	if dep.Installed && dep.Message != "" {
		status = dep.Message
	}

	fmt.Printf("\n%s is %s.\n", dep.Name, status)
	fmt.Println("Installation may require sudo privileges.")

	return promptYesNo(fmt.Sprintf("Do you want to install %s?", dep.Name))
}

// promptYesNo prompts the user for a yes/no response
func promptYesNo(question string) (bool, error) {
	reader := bufio.NewReader(os.Stdin)

	fmt.Printf("%s [y/N]: ", question)

	response, err := reader.ReadString('\n')
	if err != nil {
		return false, err
	}

	response = strings.TrimSpace(strings.ToLower(response))

	switch response {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

// PrintDependencyStatus prints a summary of dependency status
func PrintDependencyStatus(deps []DependencyStatus) {
	fmt.Println("\nCLI Tool Status:")
	fmt.Println("----------------")

	for _, dep := range deps {
		icon := "+"
		if !dep.Installed {
			icon = "-"
		} else if dep.Message != "" && strings.Contains(dep.Message, "upgrade") {
			icon = "!"
		}

		version := dep.Version
		if version == "" {
			version = "not installed"
		}

		required := ""
		if dep.Required {
			required = " (required)"
		}

		fmt.Printf("  [%s] %s: %s%s\n", icon, dep.Name, version, required)

		if dep.Message != "" {
			fmt.Printf("      %s\n", dep.Message)
		}
	}

	fmt.Println()
}

// PrintInstallationStart prints a message when starting installation
func PrintInstallationStart(name string) {
	fmt.Printf("\nInstalling %s...\n", name)
}

// PrintInstallationSuccess prints a success message after installation
func PrintInstallationSuccess(name string) {
	fmt.Printf("%s installed successfully.\n", name)
}

// PrintInstallationError prints an error message if installation fails
func PrintInstallationError(name string, err error) {
	fmt.Printf("Failed to install %s: %v\n", name, err)
}
