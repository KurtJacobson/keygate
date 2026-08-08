import { Link } from "react-router-dom"
import { Button } from "@/components/ui/button"
import { useI18n } from "@/i18n"

export default function NotFoundPage() {
  const { t } = useI18n()
  return (
    <div className="min-h-screen flex flex-col items-center justify-center gap-3 p-6 text-center bg-muted/30">
      <img src="/logo.svg" alt="" className="h-12 w-12 opacity-70" />
      <p className="text-4xl font-bold tracking-tight">404</p>
      <p className="text-lg font-medium">{t("notFound.title")}</p>
      <p className="text-sm text-muted-foreground max-w-xs italic">{t("notFound.desc")}</p>
      <Button asChild className="mt-2">
        <Link to="/login">{t("notFound.back")}</Link>
      </Button>
    </div>
  )
}
