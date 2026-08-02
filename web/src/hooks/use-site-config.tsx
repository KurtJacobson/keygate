import { createContext, type ReactNode, useContext, useEffect, useState } from "react"
import { site } from "@/lib/api"

interface SiteConfig {
  site_name: string
  brand_color: string
  logo_url: string
  favicon_url: string
  timezone: string
  language: string
  attribution_text: string
  attribution_url: string
  loading: boolean
}

const defaults: SiteConfig = {
  site_name: "Keygate",
  brand_color: "",
  logo_url: "",
  favicon_url: "",
  timezone: "UTC",
  language: "",
  attribution_text: "Powered by Keygate",
  attribution_url: "https://keygate.app",
  loading: true,
}

const SiteConfigContext = createContext<SiteConfig>(defaults)

export function SiteConfigProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<SiteConfig>(defaults)

  useEffect(() => {
    site
      .config()
      .then((data) => {
        setConfig({
          site_name: data.site_name || "Keygate",
          brand_color: data.brand_color || "",
          logo_url: data.logo_url || "",
          favicon_url: data.favicon_url || "",
          timezone: data.timezone || "UTC",
          language: data.language || "",
          attribution_text: data.attribution_text || "Powered by Keygate",
          attribution_url: data.attribution_url || "https://keygate.app",
          loading: false,
        })
        // Dynamic favicon: dedicated favicon_url wins, else fall back
        // to the logo so a single-image setup still brands the tab.
        const favicon = data.favicon_url || data.logo_url
        if (favicon) {
          // index.html declares multiple <link rel="icon"> variants and
          // browsers pick their favorite (often the sizes="32x32" one),
          // so rewriting only the first never took effect — update all.
          document.querySelectorAll<HTMLLinkElement>("link[rel~='icon']").forEach((link) => {
            link.href = favicon
          })
        }
        if (data.brand_color) {
          const root = document.documentElement.style
          root.setProperty("--color-primary", data.brand_color)
          // Derive the coordinated tints from the one picked color so
          // admins don't have to hand-pick a consistent palette. The
          // mix ratios mirror the default theme's relationship between
          // primary and its secondary/accent/ring companions.
          root.setProperty("--color-ring", data.brand_color)
          root.setProperty("--color-secondary", `color-mix(in oklch, ${data.brand_color} 6%, white)`)
          root.setProperty("--color-accent", `color-mix(in oklch, ${data.brand_color} 12%, white)`)
          root.setProperty("--color-accent-foreground", `color-mix(in oklch, ${data.brand_color} 70%, black)`)
        }
        if (data.site_name) {
          document.title = data.site_name
        }
        // Set default language if user hasn't explicitly chosen one
        if (data.language && !localStorage.getItem("keygate_locale")) {
          localStorage.setItem("keygate_locale", data.language)
          document.documentElement.lang = data.language
        }
      })
      .catch(() => setConfig({ ...defaults, loading: false }))
  }, [])

  return <SiteConfigContext.Provider value={config}>{children}</SiteConfigContext.Provider>
}

export function useSiteConfig() {
  return useContext(SiteConfigContext)
}
