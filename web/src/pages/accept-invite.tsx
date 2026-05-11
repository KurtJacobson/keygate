import { useMutation } from "@tanstack/react-query"
import { CheckCircle2, XCircle } from "lucide-react"
import { useEffect, useRef } from "react"
import { Link, useSearchParams } from "react-router-dom"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { useSiteConfig } from "@/hooks/use-site-config"
import { useI18n } from "@/i18n"
import { invites } from "@/lib/api"

export default function AcceptInvitePage() {
  const { t } = useI18n()
  const [params] = useSearchParams()
  const token = params.get("token") || ""

  // Backend collapses bad/expired/already-accepted into one error
  // shape so we don't try to distinguish the failure reason here.
  const acceptMut = useMutation({
    mutationFn: () => invites.accept(token),
  })

  // Fire exactly once per distinct token. StrictMode double-invokes
  // effects in dev; storing the LAST-fired token (not just a boolean)
  // means: same token in the URL → already fired → no-op. A pasted
  // new token → ref doesn't match → fire again. The ref-keyed-on-
  // token form also satisfies the exhaustive-deps lint cleanly:
  // mutate is referentially stable across renders for react-query,
  // and token is the only thing actually driving re-invocation.
  const fired = useRef<string | null>(null)
  const mutate = acceptMut.mutate
  useEffect(() => {
    if (!token || fired.current === token) return
    fired.current = token
    mutate()
  }, [token, mutate])

  if (!token) {
    return (
      <Shell>
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <XCircle className="h-5 w-5 text-destructive" />
              {t("acceptInvite.missingToken")}
            </CardTitle>
            <CardDescription>{t("acceptInvite.missingTokenDesc")}</CardDescription>
          </CardHeader>
          <CardContent>
            <Button asChild variant="outline">
              <Link to="/">{t("acceptInvite.goHome")}</Link>
            </Button>
          </CardContent>
        </Card>
      </Shell>
    )
  }

  if (acceptMut.isPending || acceptMut.isIdle) {
    return (
      <Shell>
        <p className="text-sm text-muted-foreground">{t("acceptInvite.processing")}</p>
      </Shell>
    )
  }

  if (acceptMut.isError) {
    return (
      <Shell>
        <Card className="max-w-md">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <XCircle className="h-5 w-5 text-destructive" />
              {t("acceptInvite.invalidTitle")}
            </CardTitle>
            <CardDescription>{t("acceptInvite.invalidDesc")}</CardDescription>
          </CardHeader>
          <CardContent>
            <Button asChild variant="outline">
              <Link to="/">{t("acceptInvite.goHome")}</Link>
            </Button>
          </CardContent>
        </Card>
      </Shell>
    )
  }

  const data = acceptMut.data!
  const productName = data.product_name || "the team"
  const roleLabel = data.role === "admin" ? t("portal.roleAdmin") : t("portal.roleMember")

  return (
    <Shell>
      <Card className="max-w-md">
        <CardHeader>
          <CardTitle className="flex items-center gap-2">
            <CheckCircle2 className="h-5 w-5 text-emerald-600" />
            {t("acceptInvite.acceptedTitle").replace("{product}", productName)}
          </CardTitle>
          <CardDescription>{t("acceptInvite.acceptedDesc").replace("{role}", roleLabel)}</CardDescription>
        </CardHeader>
        <CardContent>
          <Button asChild>
            <Link to="/portal">{t("acceptInvite.openPortal")}</Link>
          </Button>
        </CardContent>
      </Card>
    </Shell>
  )
}

function Shell({ children }: { children: React.ReactNode }) {
  const { attribution_text, attribution_url } = useSiteConfig()
  return (
    <div className="min-h-screen flex flex-col items-center justify-center p-4">
      {children}
      {/* Attribution required by AGPL v3 Section 7(b) — see NOTICE */}
      <a
        href={attribution_url}
        target="_blank"
        rel="noopener noreferrer"
        className="mt-4 text-[10px] text-muted-foreground/40 hover:text-muted-foreground transition-colors"
      >
        {attribution_text}
      </a>
    </div>
  )
}
