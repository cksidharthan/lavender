package components

// shared tailwind class sets for elements that repeat across pages.
const (
	TitleClass    = "text-2xl font-semibold tracking-tight"
	SubtitleClass = "mt-1.5 mb-6 text-slate-500 sm:text-sm"
	dialogClass   = "m-auto w-[calc(100%-2rem)] max-w-sm scale-95 rounded-2xl border border-slate-200 bg-white p-6 text-slate-950 opacity-0 shadow-2xl transition-[opacity,scale,display,overlay] duration-200 transition-discrete backdrop:bg-slate-950/40 backdrop:backdrop-blur-sm open:scale-100 open:opacity-100 starting:open:scale-95 starting:open:opacity-0"
	inputClass    = "h-11 w-full sm:h-10 rounded-lg border border-slate-200 bg-white px-3.5 text-base shadow-xs transition sm:text-sm placeholder:text-slate-400 hover:border-slate-300 focus:border-brand focus:ring-4 focus:ring-brand/15 focus:outline-none"
)
