package components

import (
	"strconv"
	"time"

	"github.com/a-h/templ"
)

// ToastContainerID is the id of the element the toasts are appended to.
const ToastContainerID = "toasts"

// DefaultToastDuration is how long a toast stays on screen when ToastProps.Duration is not set.
const DefaultToastDuration = 3 * time.Second

type ToastVariant int

const (
	ToastInfo ToastVariant = iota
	ToastSuccess
	ToastWarning
	ToastError
)

type ToastProps struct {
	Variant ToastVariant
	Title   string // optional
	Message string
	// Duration is how long the toast stays before it dismisses itself. Zero means
	// DefaultToastDuration, a negative value keeps the toast until it is dismissed.
	Duration time.Duration
}

func (p ToastProps) duration() time.Duration {
	if p.Duration == 0 {
		return DefaultToastDuration
	}

	return p.Duration
}

func (p ToastProps) autoDismiss() bool {
	return p.duration() > 0
}

func (p ToastProps) progressStyle() templ.SafeCSS {
	return templ.SafeCSS("animation-duration: " + strconv.FormatInt(p.duration().Milliseconds(), 10) + "ms")
}

// role is "alert" for errors, so screen readers announce them right away.
func (p ToastProps) role() string {
	if p.Variant == ToastError {
		return "alert"
	}

	return "status"
}

func (p ToastProps) iconClass() string {
	switch p.Variant {
	case ToastSuccess:
		return "text-emerald-500"
	case ToastWarning:
		return "text-amber-500"
	case ToastError:
		return "text-red-500"
	default:
		return "text-brand"
	}
}

func (p ToastProps) barClass() string {
	switch p.Variant {
	case ToastSuccess:
		return "bg-emerald-500"
	case ToastWarning:
		return "bg-amber-500"
	case ToastError:
		return "bg-red-500"
	default:
		return "bg-brand"
	}
}

// iconPath is the (lucide style) stroke path of the variant's icon.
func (p ToastProps) iconPath() string {
	switch p.Variant {
	case ToastSuccess:
		return "M21.8 10A10 10 0 1 1 17 3.3M9 11l3 3L22 4"
	case ToastWarning:
		return "M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0zM12 9v4M12 17h.01"
	case ToastError:
		return "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM15 9l-6 6M9 9l6 6"
	default:
		return "M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20zM12 16v-4M12 8h.01"
	}
}
