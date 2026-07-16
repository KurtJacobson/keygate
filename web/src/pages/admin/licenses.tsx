import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Ban, Check, Copy, Eye, Package, Pause, Pencil, Play, Plus, RefreshCw, Search, Trash2 } from "lucide-react"
import { useState } from "react"
import { Link } from "react-router-dom"
import { showToast } from "@/components/toast"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableEmpty,
  DataTableHead,
  DataTableHeader,
  DataTablePagination,
  DataTableRow,
} from "@/components/ui/data-table"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useI18n } from "@/i18n"
import { admin } from "@/lib/api"
import { formatDate, statusColor } from "@/lib/utils"

export default function LicensesPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [productFilter, setProductFilter] = useState<string>("")
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [search, setSearch] = useState("")
  const [page, setPage] = useState(0)
  const limit = 20

  const { data: productsData } = useQuery({ queryKey: ["admin", "products"], queryFn: () => admin.listProducts() })
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "licenses", productFilter, statusFilter, search, page],
    queryFn: () =>
      admin.listLicenses({
        product_id: productFilter || undefined,
        status: statusFilter || undefined,
        search: search || undefined,
        offset: page * limit,
        limit,
      }),
  })

  const [creating, setCreating] = useState(false)
  const [viewing, setViewing] = useState<string | null>(null)

  const products = productsData?.products || []
  const licenses = data?.licenses || []
  const total = data?.total || 0
  const totalPages = Math.ceil(total / limit)

  const createMut = useMutation({
    mutationFn: admin.createLicense,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "licenses"] })
      setCreating(false)
      showToast(t("toast.licenseCreated"), "success")
    },
  })

  if (!isLoading && products.length === 0) {
    return (
      <div className="space-y-6">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">{t("licenses.title")}</h1>
          <p className="text-muted-foreground">Manage software licenses.</p>
        </div>
        <Card>
          <CardContent className="py-12 text-center">
            <Package className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <p className="text-lg font-medium">{t("licenses.noProducts")}</p>
            <p className="text-muted-foreground mt-1 mb-4">{t("licenses.noProductsDesc")}</p>
            <Button asChild>
              <Link to="/admin/products">
                <Plus className="h-4 w-4 mr-2" /> Create Product
              </Link>
            </Button>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-2xl font-bold tracking-tight">{t("licenses.title")}</h1>
          <p className="text-muted-foreground">
            {total} {t("licenses.title").toLowerCase()} total
          </p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("licenses.issue")}
        </Button>
      </div>

      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("common.search")}
            value={search}
            onChange={(e) => {
              setSearch(e.target.value)
              setPage(0)
            }}
            className="pl-9"
          />
        </div>
        <Select
          value={productFilter}
          onValueChange={(v) => {
            setProductFilter(v === "all" ? "" : v)
            setPage(0)
          }}
        >
          <SelectTrigger className="w-48">
            <SelectValue placeholder={t("filter.allProducts")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("filter.allProducts")}</SelectItem>
            {products.map((p) => (
              <SelectItem key={p.id} value={p.id}>
                {p.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={statusFilter}
          onValueChange={(v) => {
            setStatusFilter(v === "all" ? "" : v)
            setPage(0)
          }}
        >
          <SelectTrigger className="w-40">
            <SelectValue placeholder={t("filter.allStatuses")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("filter.allStatuses")}</SelectItem>
            {["active", "trialing", "past_due", "canceled", "expired", "suspended", "revoked"].map((s) => (
              <SelectItem key={s} value={s}>
                {t(`status.${s}` as any)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-64 animate-pulse bg-muted rounded-lg" />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("common.email")}</DataTableHead>
                    <DataTableHead>{t("licenses.licenseKey")}</DataTableHead>
                    <DataTableHead>{t("common.product")}</DataTableHead>
                    <DataTableHead>{t("common.plan")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("licenses.validUntil")}</DataTableHead>
                    <DataTableHead>{t("common.created")}</DataTableHead>
                    <DataTableHead className="w-16" />
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {licenses.length === 0 && <DataTableEmpty colSpan={8} message={t("licenses.empty")} />}
                  {licenses.map((lic) => (
                    <DataTableRow key={lic.id}>
                      <DataTableCell className="font-medium">{lic.email}</DataTableCell>
                      <DataTableCell>
                        <code className="text-xs bg-muted px-1.5 py-0.5 rounded">{lic.license_key}</code>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{lic.product?.name || "-"}</DataTableCell>
                      <DataTableCell className="text-muted-foreground">{lic.plan?.name || "-"}</DataTableCell>
                      <DataTableCell>
                        <Badge className={statusColor(lic.status)}>{t(`status.${lic.status}` as any)}</Badge>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground text-xs">
                        {formatDate(lic.valid_until)}
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground text-xs">
                        {formatDate(lic.created_at)}
                      </DataTableCell>
                      <DataTableCell>
                        <Button variant="ghost" size="icon" onClick={() => setViewing(lic.id)}>
                          <Eye className="h-4 w-4" />
                        </Button>
                      </DataTableCell>
                    </DataTableRow>
                  ))}
                </DataTableBody>
              </DataTable>
              {total > 0 && (
                <DataTablePagination
                  page={page}
                  totalPages={totalPages}
                  total={total}
                  pageSize={limit}
                  onPageChange={setPage}
                />
              )}
            </>
          )}
        </CardContent>
      </Card>

      {/* Create */}
      {creating && (
        <CreateLicenseDialog
          open
          onClose={() => setCreating(false)}
          products={products}
          onSubmit={(d) => createMut.mutate(d)}
          loading={createMut.isPending}
        />
      )}

      {/* Detail */}
      {viewing && <LicenseDetail id={viewing} onClose={() => setViewing(null)} />}
    </div>
  )
}

function CreateLicenseDialog({
  open,
  onClose,
  products,
  onSubmit,
  loading,
}: {
  open: boolean
  onClose: () => void
  products: { id: string; name: string; type?: string }[]
  onSubmit: (d: {
    product_id: string
    plan_id: string
    email: string
    notes?: string
    external_customer_id?: string
    external_workspace_id?: string
    valid_until?: string
  }) => void
  loading: boolean
}) {
  const { t } = useI18n()
  const [productId, setProductId] = useState(products[0]?.id || "")
  const [email, setEmail] = useState("")
  const [notes, setNotes] = useState("")
  const [planId, setPlanId] = useState("")
  const [externalCustomerID, setExternalCustomerID] = useState("")
  const [externalWorkspaceID, setExternalWorkspaceID] = useState("")
  const [validUntil, setValidUntil] = useState("")

  const { data: plansData } = useQuery({
    queryKey: ["admin", "plans", productId],
    queryFn: () => admin.listPlans(productId),
    enabled: !!productId,
  })
  const plans = plansData?.plans || []

  // Show a hint right under the product dropdown so admin knows what
  // they're committing to — saas licenses don't get device activation,
  // desktop licenses don't get seats, etc. Mirrors the backend
  // capability gate so admins can predict downstream behavior.
  const selectedProduct = products.find((p) => p.id === productId)
  const productType = selectedProduct?.type
  const typeHintKey =
    productType === "desktop"
      ? "products.desktopDesc"
      : productType === "saas"
        ? "products.saasDesc"
        : productType === "hybrid"
          ? "products.hybridDesc"
          : null

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("licenses.issue")}</DialogTitle>
          <DialogDescription>{t("licenses.issueDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            onSubmit({
              product_id: productId,
              plan_id: planId,
              email,
              notes,
              external_customer_id: externalCustomerID.trim() || undefined,
              external_workspace_id: externalWorkspaceID.trim() || undefined,
              // Date-only input → expire at end of that day, UTC. Good
              // enough for invoice-style licensing without a time picker.
              valid_until: validUntil ? `${validUntil}T23:59:59Z` : undefined,
            })
          }}
          className="space-y-4"
        >
          <div className="space-y-2">
            <Label>{t("common.product")}</Label>
            <Select
              value={productId}
              onValueChange={(v) => {
                setProductId(v)
                setPlanId("")
              }}
            >
              <SelectTrigger>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {products.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                    {p.type ? <span className="ml-2 text-xs text-muted-foreground">[{p.type}]</span> : null}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            {typeHintKey && <p className="text-xs text-muted-foreground">{t(typeHintKey)}</p>}
          </div>
          <div className="space-y-2">
            <Label>{t("common.plan")}</Label>
            <Select value={planId} onValueChange={setPlanId}>
              <SelectTrigger>
                <SelectValue placeholder="Select a plan" />
              </SelectTrigger>
              <SelectContent>
                {plans.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="space-y-2">
            <Label>{t("common.email")}</Label>
            <Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          </div>
          <div className="space-y-2">
            <Label>{t("licenses.notesOptional")}</Label>
            <Input value={notes} onChange={(e) => setNotes(e.target.value)} />
          </div>
          <div className="space-y-2">
            <Label>{t("licenses.validUntilOptional")}</Label>
            <Input
              type="date"
              value={validUntil}
              min={new Date().toISOString().slice(0, 10)}
              onChange={(e) => setValidUntil(e.target.value)}
            />
            <p className="text-xs text-muted-foreground">{t("licenses.validUntilHint")}</p>
          </div>
          {/* External identifiers — opaque strings the merchant uses
              to map their own user/workspace model to this license.
              Both optional; leave blank if not integrating with an
              external system. */}
          <div className="grid grid-cols-2 gap-3">
            <div className="space-y-2">
              <Label>{t("licenses.externalCustomerID")}</Label>
              <Input
                value={externalCustomerID}
                onChange={(e) => setExternalCustomerID(e.target.value)}
                placeholder={t("licenses.externalIDPlaceholder")}
                maxLength={256}
              />
            </div>
            <div className="space-y-2">
              <Label>{t("licenses.externalWorkspaceID")}</Label>
              <Input
                value={externalWorkspaceID}
                onChange={(e) => setExternalWorkspaceID(e.target.value)}
                placeholder={t("licenses.externalIDPlaceholder")}
                maxLength={256}
              />
            </div>
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={loading || !planId}>
              {loading ? t("common.loading") : t("licenses.issue")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function LicenseDetail({ id, onClose }: { id: string; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data: lic, isLoading } = useQuery({ queryKey: ["admin", "license", id], queryFn: () => admin.getLicense(id) })
  const [copied, setCopied] = useState(false)
  const [changingPlan, setChangingPlan] = useState(false)
  // null = not editing; "" = editing with empty value (perpetual)
  const [editingValidUntil, setEditingValidUntil] = useState<string | null>(null)

  const revokeMut = useMutation({
    mutationFn: () => admin.revokeLicense(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin"] })
    },
  })
  const suspendMut = useMutation({
    mutationFn: () => admin.suspendLicense(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin"] })
    },
  })
  const reinstateMut = useMutation({
    mutationFn: () => admin.reinstateLicense(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin"] })
    },
  })
  const refundMut = useMutation({
    mutationFn: () => admin.refundLicense(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin"] })
    },
  })
  const validUntilMut = useMutation({
    mutationFn: (validUntil: string) => admin.setLicenseValidUntil(id, validUntil),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin"] })
      setEditingValidUntil(null)
    },
  })
  // Activation deletion is destructive — wrap in a confirmation
  // state so a stray ghost-click on the trash icon (icons sit
  // close together in the row) doesn't immediately revoke a device.
  const [confirmDeactivation, setConfirmDeactivation] = useState<{ id: string; label: string } | null>(null)
  const deleteActMut = useMutation({
    mutationFn: admin.deleteActivation,
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "license", id] })
      setConfirmDeactivation(null)
    },
  })

  const copyKey = () => {
    if (lic) {
      navigator.clipboard.writeText(lic.license_key)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    }
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-2xl max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t("licenses.detail")}</DialogTitle>
          <DialogDescription>{lic?.email}</DialogDescription>
        </DialogHeader>
        {isLoading || !lic ? (
          <div className="h-48 animate-pulse bg-muted rounded-lg" />
        ) : (
          <Tabs defaultValue="info">
            <TabsList>
              <TabsTrigger value="info">{t("licenses.info")}</TabsTrigger>
              <TabsTrigger value="usage">{t("licenses.usage")}</TabsTrigger>
              <TabsTrigger value="seats">{t("licenses.seats")}</TabsTrigger>
            </TabsList>

            <TabsContent value="info">
              <div className="space-y-6">
                {/* Info */}
                <div className="grid grid-cols-2 gap-4 text-sm">
                  <div>
                    <p className="text-muted-foreground">{t("licenses.licenseKey")}</p>
                    <div className="flex items-center gap-2 mt-1">
                      <code className="bg-muted px-2 py-1 rounded text-xs">{lic.license_key}</code>
                      <Button variant="ghost" size="icon" className="h-6 w-6" onClick={copyKey}>
                        {copied ? <Check className="h-3 w-3 text-emerald-600" /> : <Copy className="h-3 w-3" />}
                      </Button>
                    </div>
                  </div>
                  <div>
                    <p className="text-muted-foreground">{t("common.status")}</p>
                    <Badge className={`mt-1 ${statusColor(lic.status)}`}>{t(`status.${lic.status}` as any)}</Badge>
                  </div>
                  <div>
                    <p className="text-muted-foreground">{t("common.product")}</p>
                    <p className="mt-1 font-medium">{lic.product?.name || "-"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">{t("common.plan")}</p>
                    <p className="mt-1 font-medium">{lic.plan?.name || "-"}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">{t("licenses.validFrom")}</p>
                    <p className="mt-1">{formatDate(lic.valid_from)}</p>
                  </div>
                  <div>
                    <p className="text-muted-foreground">{t("licenses.validUntil")}</p>
                    {editingValidUntil === null ? (
                      <div className="flex items-center gap-1 mt-1">
                        <p>{formatDate(lic.valid_until)}</p>
                        {/* Stripe owns the expiry for subscription licenses —
                            editing it here would be overwritten on renewal. */}
                        {lic.payment_provider !== "stripe" && (
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-6 w-6"
                            title={t("licenses.validUntilEdit")}
                            onClick={() => setEditingValidUntil(lic.valid_until ? lic.valid_until.slice(0, 10) : "")}
                          >
                            <Pencil className="h-3 w-3" />
                          </Button>
                        )}
                      </div>
                    ) : (
                      <div className="mt-1 space-y-1">
                        <div className="flex items-center gap-1">
                          <Input
                            type="date"
                            className="h-7 w-40 text-xs"
                            value={editingValidUntil}
                            onChange={(e) => setEditingValidUntil(e.target.value)}
                          />
                          <Button
                            size="sm"
                            className="h-7"
                            disabled={validUntilMut.isPending}
                            onClick={() =>
                              validUntilMut.mutate(editingValidUntil ? `${editingValidUntil}T23:59:59Z` : "")
                            }
                          >
                            {t("common.save")}
                          </Button>
                          <Button
                            size="sm"
                            variant="ghost"
                            className="h-7"
                            onClick={() => setEditingValidUntil(null)}
                          >
                            {t("common.cancel")}
                          </Button>
                        </div>
                        <p className="text-xs text-muted-foreground">{t("licenses.validUntilClear")}</p>
                      </div>
                    )}
                  </div>
                  {lic.payment_provider && (
                    <div>
                      <p className="text-muted-foreground">{t("licenses.payment")}</p>
                      <Badge variant="secondary" className="mt-1">
                        {lic.payment_provider}
                      </Badge>
                    </div>
                  )}
                  <div>
                    <p className="text-muted-foreground">{t("common.created")}</p>
                    <p className="mt-1">{formatDate(lic.created_at)}</p>
                  </div>
                </div>
                {lic.notes && (
                  <div className="text-sm">
                    <p className="text-muted-foreground">{t("licenses.notes")}</p>
                    <p className="mt-1">{lic.notes}</p>
                  </div>
                )}
                {(lic.external_customer_id || lic.external_workspace_id) && (
                  <div className="grid grid-cols-2 gap-4 text-sm">
                    {lic.external_customer_id && (
                      <div>
                        <p className="text-muted-foreground">{t("licenses.externalCustomerID")}</p>
                        <p className="mt-1 font-mono text-xs break-all">{lic.external_customer_id}</p>
                      </div>
                    )}
                    {lic.external_workspace_id && (
                      <div>
                        <p className="text-muted-foreground">{t("licenses.externalWorkspaceID")}</p>
                        <p className="mt-1 font-mono text-xs break-all">{lic.external_workspace_id}</p>
                      </div>
                    )}
                  </div>
                )}

                {/* Actions */}
                <div className="flex gap-2 flex-wrap">
                  {(lic.status === "active" || lic.status === "trialing") && (
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button variant="outline" size="sm" disabled={suspendMut.isPending}>
                          <Pause className="h-4 w-4 mr-1" /> {t("licenses.suspend")}
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>{t("licenses.suspend")}</AlertDialogTitle>
                          <AlertDialogDescription>{t("licenses.suspendConfirm")}</AlertDialogDescription>
                        </AlertDialogHeader>
                        <div className="flex justify-end gap-2 mt-4">
                          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
                          <AlertDialogAction onClick={() => suspendMut.mutate()}>
                            {t("licenses.suspend")}
                          </AlertDialogAction>
                        </div>
                      </AlertDialogContent>
                    </AlertDialog>
                  )}
                  {lic.status === "suspended" && (
                    <Button
                      variant="outline"
                      size="sm"
                      onClick={() => reinstateMut.mutate()}
                      disabled={reinstateMut.isPending}
                    >
                      <Play className="h-4 w-4 mr-1" /> {t("licenses.reinstate")}
                    </Button>
                  )}
                  {lic.status !== "revoked" && (
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button variant="destructive" size="sm" disabled={revokeMut.isPending}>
                          <Ban className="h-4 w-4 mr-1" /> {t("licenses.revoke")}
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>{t("licenses.revoke")}</AlertDialogTitle>
                          <AlertDialogDescription>{t("licenses.revokeConfirm")}</AlertDialogDescription>
                        </AlertDialogHeader>
                        <div className="flex justify-end gap-2 mt-4">
                          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
                          <AlertDialogAction
                            className="bg-destructive text-white hover:bg-destructive/90"
                            onClick={() => revokeMut.mutate()}
                          >
                            {t("licenses.revoke")}
                          </AlertDialogAction>
                        </div>
                      </AlertDialogContent>
                    </AlertDialog>
                  )}
                  {lic.payment_provider && lic.status !== "revoked" && (
                    <AlertDialog>
                      <AlertDialogTrigger asChild>
                        <Button
                          variant="outline"
                          size="sm"
                          className="text-destructive border-destructive"
                          disabled={refundMut.isPending}
                        >
                          {t("licenses.refund")}
                        </Button>
                      </AlertDialogTrigger>
                      <AlertDialogContent>
                        <AlertDialogHeader>
                          <AlertDialogTitle>{t("licenses.refund")}</AlertDialogTitle>
                          <AlertDialogDescription>{t("licenses.refundConfirm")}</AlertDialogDescription>
                        </AlertDialogHeader>
                        <div className="flex justify-end gap-2 mt-4">
                          <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
                          <AlertDialogAction
                            className="bg-destructive text-white hover:bg-destructive/90"
                            onClick={() => refundMut.mutate()}
                          >
                            {t("licenses.refund")}
                          </AlertDialogAction>
                        </div>
                      </AlertDialogContent>
                    </AlertDialog>
                  )}
                  <Button variant="outline" size="sm" onClick={() => setChangingPlan(true)}>
                    {t("licenses.changePlan")}
                  </Button>
                </div>

                {/* Activations — hidden for SaaS products (which don't
                    use per-device activation; their user model is seats). */}
                {lic.product?.type !== "saas" && (
                  <>
                    <Separator />
                    <div>
                      <h3 className="font-semibold mb-3">
                        {t("licenses.activations")} ({lic.activations?.length || 0} / {lic.plan?.max_activations || "-"}
                        )
                      </h3>
                      {lic.activations && lic.activations.length > 0 ? (
                        <DataTable>
                          <DataTableHeader>
                            <DataTableRow>
                              <DataTableHead>{t("licenses.identifier")}</DataTableHead>
                              <DataTableHead>{t("licenses.type")}</DataTableHead>
                              <DataTableHead>{t("licenses.label")}</DataTableHead>
                              <DataTableHead>{t("licenses.lastVerified")}</DataTableHead>
                              <DataTableHead className="w-12" />
                            </DataTableRow>
                          </DataTableHeader>
                          <DataTableBody>
                            {lic.activations.map((act) => (
                              <DataTableRow key={act.id}>
                                <DataTableCell>
                                  <code className="text-xs">{act.identifier}</code>
                                </DataTableCell>
                                <DataTableCell>
                                  <Badge variant="secondary">{act.identifier_type}</Badge>
                                </DataTableCell>
                                <DataTableCell className="text-muted-foreground">{act.label || "-"}</DataTableCell>
                                <DataTableCell className="text-muted-foreground text-xs">
                                  {formatDate(act.last_verified)}
                                </DataTableCell>
                                <DataTableCell>
                                  <Button
                                    variant="ghost"
                                    size="icon"
                                    className="h-7 w-7"
                                    onClick={() =>
                                      setConfirmDeactivation({ id: act.id, label: act.label || act.identifier })
                                    }
                                    disabled={deleteActMut.isPending && deleteActMut.variables === act.id}
                                  >
                                    <Trash2 className="h-3 w-3 text-destructive" />
                                  </Button>
                                </DataTableCell>
                              </DataTableRow>
                            ))}
                          </DataTableBody>
                        </DataTable>
                      ) : (
                        <p className="text-sm text-muted-foreground">{t("licenses.noActivations")}</p>
                      )}
                    </div>
                  </>
                )}

                {/* Entitlements */}
                {lic.plan?.entitlements && lic.plan.entitlements.length > 0 && (
                  <>
                    <Separator />
                    <div>
                      <h3 className="font-semibold mb-3">{t("plans.entitlements")}</h3>
                      <div className="grid grid-cols-2 gap-2">
                        {lic.plan.entitlements.map((e) => (
                          <div key={e.id} className="flex justify-between bg-muted/50 rounded px-3 py-2 text-sm">
                            <span className="font-medium">{e.feature}</span>
                            <span className="text-muted-foreground">{e.value}</span>
                          </div>
                        ))}
                      </div>
                    </div>
                  </>
                )}
              </div>
            </TabsContent>

            <TabsContent value="usage">
              <UsageTab licenseId={id} />
            </TabsContent>

            <TabsContent value="seats">
              <SeatsTab licenseId={id} maxSeats={lic?.plan?.max_seats || 0} />
            </TabsContent>
          </Tabs>
        )}

        {/* Change Plan Dialog */}
        {changingPlan && lic && (
          <ChangePlanDialog
            licenseId={id}
            productId={lic.product_id}
            currentPlanId={lic.plan_id}
            onClose={() => setChangingPlan(false)}
          />
        )}
        <AlertDialog open={!!confirmDeactivation} onOpenChange={() => setConfirmDeactivation(null)}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>
                {t("licenses.deactivateTitle", { device: confirmDeactivation?.label || "" })}
              </AlertDialogTitle>
              <AlertDialogDescription>{t("licenses.deactivateDesc")}</AlertDialogDescription>
            </AlertDialogHeader>
            <div className="flex justify-end gap-2">
              <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
              <AlertDialogAction
                className="bg-destructive text-white hover:bg-destructive/90"
                onClick={() => confirmDeactivation && deleteActMut.mutate(confirmDeactivation.id)}
              >
                {t("licenses.deactivate")}
              </AlertDialogAction>
            </div>
          </AlertDialogContent>
        </AlertDialog>
      </DialogContent>
    </Dialog>
  )
}

function UsageTab({ licenseId }: { licenseId: string }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [page, setPage] = useState(0)
  const limit = 20
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "license-usage", licenseId, page],
    queryFn: () => admin.getLicenseUsage(licenseId, { offset: page * limit, limit }),
  })

  // confirmResetUsage holds the feature name pending confirmation.
  // Reset is destructive: it zeroes the counter for the live period,
  // which most plans will treat as "free re-use until the next
  // reset" — a stray click can effectively grant the customer free
  // usage. The AlertDialog forces an intentional click.
  const [confirmResetUsage, setConfirmResetUsage] = useState<string | null>(null)
  const resetMut = useMutation({
    mutationFn: (feature: string) => admin.resetUsageCounter(licenseId, { feature }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "license-usage", licenseId] })
      setConfirmResetUsage(null)
    },
  })

  const counters = data?.counters || []
  const events = data?.events || []
  const total = data?.total || 0
  const totalPages = Math.ceil(total / limit)

  if (isLoading) return <div className="h-32 animate-pulse bg-muted rounded-lg" />

  return (
    <div className="space-y-6 pt-2">
      {/* Counters */}
      <div>
        <h3 className="font-semibold mb-3">{t("licenses.usageCounters")}</h3>
        {counters.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("licenses.noUsageCounters")}</p>
        ) : (
          <DataTable>
            <DataTableHeader>
              <DataTableRow>
                <DataTableHead>{t("licenses.feature")}</DataTableHead>
                <DataTableHead>{t("licenses.period")}</DataTableHead>
                <DataTableHead>{t("licenses.periodKey")}</DataTableHead>
                <DataTableHead>{t("licenses.used")}</DataTableHead>
                <DataTableHead>{t("licenses.updated")}</DataTableHead>
                <DataTableHead className="w-12" />
              </DataTableRow>
            </DataTableHeader>
            <DataTableBody>
              {counters.map((c) => (
                <DataTableRow key={c.id}>
                  <DataTableCell className="font-medium">{c.feature}</DataTableCell>
                  <DataTableCell>
                    <Badge variant="secondary">{c.period}</Badge>
                  </DataTableCell>
                  <DataTableCell className="text-muted-foreground text-xs">{c.period_key}</DataTableCell>
                  <DataTableCell className="font-medium">{c.used}</DataTableCell>
                  <DataTableCell className="text-muted-foreground text-xs">{formatDate(c.updated_at)}</DataTableCell>
                  <DataTableCell>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-7 w-7"
                      title={t("licenses.resetCounter")}
                      onClick={() => setConfirmResetUsage(c.feature)}
                      disabled={resetMut.isPending && resetMut.variables === c.feature}
                    >
                      <RefreshCw className="h-3 w-3" />
                    </Button>
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </div>

      <Separator />

      {/* Recent events */}
      <div>
        <h3 className="font-semibold mb-3">{t("licenses.recentEvents")}</h3>
        {events.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t("licenses.noUsageEvents")}</p>
        ) : (
          <>
            <DataTable>
              <DataTableHeader>
                <DataTableRow>
                  <DataTableHead>{t("licenses.feature")}</DataTableHead>
                  <DataTableHead>{t("licenses.quantity")}</DataTableHead>
                  <DataTableHead>{t("licenses.ip")}</DataTableHead>
                  <DataTableHead>{t("licenses.recorded")}</DataTableHead>
                </DataTableRow>
              </DataTableHeader>
              <DataTableBody>
                {events.map((e) => (
                  <DataTableRow key={e.id}>
                    <DataTableCell className="font-medium">{e.feature}</DataTableCell>
                    <DataTableCell>{e.quantity}</DataTableCell>
                    <DataTableCell className="text-muted-foreground text-xs">{e.ip_address || "-"}</DataTableCell>
                    <DataTableCell className="text-muted-foreground text-xs">{formatDate(e.recorded_at)}</DataTableCell>
                  </DataTableRow>
                ))}
              </DataTableBody>
            </DataTable>
            {total > 0 && (
              <DataTablePagination
                page={page}
                totalPages={totalPages}
                total={total}
                pageSize={limit}
                onPageChange={setPage}
              />
            )}
          </>
        )}
      </div>
      <AlertDialog open={!!confirmResetUsage} onOpenChange={() => setConfirmResetUsage(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("licenses.resetCounterTitle", { feature: confirmResetUsage || "" })}</AlertDialogTitle>
            <AlertDialogDescription>{t("licenses.resetCounterDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => confirmResetUsage && resetMut.mutate(confirmResetUsage)}>
              {t("licenses.resetCounter")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// SeatsTab is READ-ONLY by design.
//
// Keygate's bundled UI doesn't expose seat / team management on
// purpose: the platform's job is license-key issuance, not team
// management. The seats data model + API endpoints exist for
// merchant integrations that want to wire team UX into their own
// product, but our admin console only reads the current state for
// support / debugging ("how many seats has ACME used?"). Inviting
// someone onto a customer's license from this view would let an
// operator change a team's composition without consent.
function SeatsTab({ licenseId, maxSeats }: { licenseId: string; maxSeats: number }) {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "license-seats", licenseId],
    queryFn: () => admin.getLicenseSeats(licenseId),
  })

  const seats = data?.seats || []
  const activeCount = data?.active_count ?? 0

  if (isLoading) return <div className="h-32 animate-pulse bg-muted rounded-lg" />

  return (
    <div className="space-y-4 pt-2">
      <h3 className="font-semibold">
        {t("licenses.seats")} ({activeCount}
        {maxSeats > 0 ? ` / ${maxSeats}` : ""} {t("licenses.active")})
      </h3>
      {seats.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t("licenses.noSeats")}</p>
      ) : (
        <DataTable>
          <DataTableHeader>
            <DataTableRow>
              <DataTableHead>{t("common.email")}</DataTableHead>
              <DataTableHead>{t("licenses.role")}</DataTableHead>
              <DataTableHead>{t("common.status")}</DataTableHead>
              <DataTableHead>{t("licenses.invited")}</DataTableHead>
              <DataTableHead>{t("licenses.accepted")}</DataTableHead>
            </DataTableRow>
          </DataTableHeader>
          <DataTableBody>
            {seats.map((s) => (
              <DataTableRow key={s.id}>
                <DataTableCell className="font-medium">{s.email}</DataTableCell>
                <DataTableCell>
                  <Badge variant="secondary">{s.role}</Badge>
                </DataTableCell>
                <DataTableCell>
                  {s.removed_at ? (
                    <Badge className="bg-red-100 text-red-800">{t("licenses.removed")}</Badge>
                  ) : s.accepted_at ? (
                    <Badge className="bg-emerald-100 text-emerald-800">{t("licenses.active")}</Badge>
                  ) : (
                    <Badge className="bg-amber-100 text-amber-800">{t("licenses.pending")}</Badge>
                  )}
                </DataTableCell>
                <DataTableCell className="text-muted-foreground text-xs">{formatDate(s.invited_at)}</DataTableCell>
                <DataTableCell className="text-muted-foreground text-xs">{formatDate(s.accepted_at)}</DataTableCell>
              </DataTableRow>
            ))}
          </DataTableBody>
        </DataTable>
      )}
    </div>
  )
}

function ChangePlanDialog({
  licenseId,
  productId,
  currentPlanId,
  onClose,
}: {
  licenseId: string
  productId: string
  currentPlanId: string
  onClose: () => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [planId, setPlanId] = useState(currentPlanId)
  const { data: plansData } = useQuery({
    queryKey: ["admin", "plans", productId],
    queryFn: () => admin.listPlans(productId),
  })
  const plans = plansData?.plans || []

  const changeMut = useMutation({
    mutationFn: () => admin.changeLicensePlan(licenseId, { plan_id: planId }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin"] })
      onClose()
    },
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("licenses.changePlan")}</DialogTitle>
          <DialogDescription>{t("licenses.changePlanDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            changeMut.mutate()
          }}
          className="space-y-4"
        >
          <div className="space-y-2">
            <Label>{t("common.plan")}</Label>
            <Select value={planId} onValueChange={setPlanId}>
              <SelectTrigger>
                <SelectValue placeholder="Select a plan" />
              </SelectTrigger>
              <SelectContent>
                {plans.map((p) => (
                  <SelectItem key={p.id} value={p.id}>
                    {p.name} {p.id === currentPlanId ? "(current)" : ""}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="flex justify-end gap-2 pt-2">
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={changeMut.isPending || planId === currentPlanId}>
              {changeMut.isPending ? t("common.loading") : t("licenses.changePlan")}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  )
}
