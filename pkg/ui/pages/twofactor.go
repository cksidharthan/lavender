package pages

import "strconv"

// TwoFactorState is what the two factor authentication card shows.
type TwoFactorState struct {
	Enabled      bool
	RecoveryLeft int
	// Setup is set while a secret waits for the code of the app that turns it on.
	Setup *TwoFactorSetup
	// Codes are the recovery codes that were just made, they are shown once.
	Codes []string
}

// TwoFactorSetup is what the user needs to add the account to an authenticator app.
type TwoFactorSetup struct {
	QR     string // the address of the image of the QR code
	Secret string // to type in, when the QR code can not be scanned
}

func recoveryText(n int) string {
	if n == 1 {
		return "1 recovery code left"
	}

	return strconv.Itoa(n) + " recovery codes left"
}

// recoveryFile is the text of the file of the recovery codes, for the download.
func recoveryFile(codes []string) string {
	text := "Lavender recovery codes\n\nEach one works once. Keep them safe.\n\n"
	for _, c := range codes {
		text += c + "\n"
	}

	return text
}

// recoveryClass warns when the recovery codes are nearly all used.
func recoveryClass(left int) string {
	if left <= 2 {
		return "font-medium text-amber-700"
	}

	return "text-slate-500"
}
