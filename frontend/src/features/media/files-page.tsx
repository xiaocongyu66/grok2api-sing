import { useQuery } from "@tanstack/react-query";
import { Database, HardDrive, Image as ImageIcon, RefreshCw, Search, Video, type LucideIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Spinner } from "@/components/ui/spinner";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { getImageStats, getMediaStorageInfo, getVideoStats, listImages, listVideos } from "@/features/media/media-api";
import type { MediaAssetDTO, MediaJobDTO } from "@/features/media/types";
import { EmptyState, ErrorState, LoadingState } from "@/shared/components/data-state";
import { PageHeader } from "@/shared/components/page-header";
import { Pagination } from "@/shared/components/pagination";
import { useDebouncedValue } from "@/shared/hooks/use-debounced-value";
import { formatDateTime, formatNumber } from "@/shared/lib/format";

type TabKey = "images" | "videos";

export function FilesPage() {
  const { t, i18n } = useTranslation();
  const [tab, setTab] = useState<TabKey>("images");
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(20);
  const [search, setSearch] = useState("");
  const debouncedSearch = useDebouncedValue(search);
  const normalizedSearch = debouncedSearch.trim();

  const storageQuery = useQuery({
    queryKey: ["media", "storage"],
    queryFn: getMediaStorageInfo,
    staleTime: 60_000,
  });
  const imageStatsQuery = useQuery({
    queryKey: ["media", "images", "stats"],
    queryFn: getImageStats,
    staleTime: 30_000,
  });
  const videoStatsQuery = useQuery({
    queryKey: ["media", "videos", "stats"],
    queryFn: getVideoStats,
    staleTime: 30_000,
  });
  const imagesQuery = useQuery({
    queryKey: ["media", "files", "images", page, pageSize, normalizedSearch],
    queryFn: () => listImages({ page, pageSize, search: normalizedSearch || undefined }),
    enabled: tab === "images",
  });
  const videosQuery = useQuery({
    queryKey: ["media", "files", "videos", page, pageSize, normalizedSearch],
    queryFn: () => listVideos({ page, pageSize, search: normalizedSearch || undefined }),
    enabled: tab === "videos",
  });

  const activeQuery = tab === "images" ? imagesQuery : videosQuery;
  const refreshing = activeQuery.isFetching || storageQuery.isFetching || imageStatsQuery.isFetching || videoStatsQuery.isFetching;

  const storageDriver = storageQuery.data?.driver || "local";
  const storageLabel = storageQuery.data?.label || "—";

  const metrics = useMemo(() => ([
    {
      icon: HardDrive,
      label: t("media.files.storageDriver"),
      value: storageDriver === "r2" ? "Cloudflare R2" : t("media.files.localDisk"),
      detail: storageLabel,
      loading: storageQuery.isPending,
    },
    {
      icon: ImageIcon,
      label: t("media.images.totalImages"),
      value: formatNumber(imageStatsQuery.data?.totalImages ?? 0, i18n.language, 0),
      detail: formatBytes(imageStatsQuery.data?.totalBytes ?? 0, i18n.language),
      loading: imageStatsQuery.isPending,
    },
    {
      icon: Video,
      label: t("media.videos.totalJobs"),
      value: formatNumber(videoStatsQuery.data?.totalJobs ?? 0, i18n.language, 0),
      detail: t("media.videos.completedDetail", { count: videoStatsQuery.data?.completed ?? 0 }),
      loading: videoStatsQuery.isPending,
    },
    {
      icon: Database,
      label: t("media.files.capacityHint"),
      value: storageDriver === "r2" ? "R2" : formatBytes(imageStatsQuery.data?.totalBytes ?? 0, i18n.language),
      detail: storageDriver === "r2" ? t("media.files.r2Hint") : t("media.images.totalBytesDetail"),
      loading: storageQuery.isPending || imageStatsQuery.isPending,
    },
  ]), [storageDriver, storageLabel, storageQuery.isPending, imageStatsQuery.data, imageStatsQuery.isPending, videoStatsQuery.data, videoStatsQuery.isPending, t, i18n.language]);

  function refreshAll(): void {
    void storageQuery.refetch();
    void imageStatsQuery.refetch();
    void videoStatsQuery.refetch();
    void activeQuery.refetch();
  }

  function changeTab(next: string): void {
    setTab(next as TabKey);
    setPage(1);
  }

  return (
    <div className="space-y-8">
      <PageHeader
        title={t("media.files.title")}
        description={t("media.files.description")}
        actions={(
          <div className="flex max-w-full flex-wrap items-center justify-end gap-1.5">
            <Button variant="secondary" size="sm" asChild>
              <Link to="/gallery">{t("nav.gallery")}</Link>
            </Button>
            <Button variant="secondary" size="sm" asChild>
              <Link to="/video-gallery">{t("nav.videoGallery")}</Link>
            </Button>
            <Button variant="secondary" size="sm" onClick={refreshAll} disabled={refreshing}>
              <RefreshCw className={refreshing ? "animate-spin" : undefined} />
              {t("common.refresh")}
            </Button>
          </div>
        )}
      />

      <section className="grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
        {metrics.map((item) => (
          <MediaMetric key={item.label} icon={item.icon} loading={item.loading} label={item.label} value={item.value} detail={item.detail} />
        ))}
      </section>

      <section className="space-y-4">
        <div className="flex min-h-12 flex-wrap items-center justify-between gap-3 py-2">
          <Tabs value={tab} onValueChange={changeTab}>
            <TabsList>
              <TabsTrigger value="images">{t("media.files.tabImages")}</TabsTrigger>
              <TabsTrigger value="videos">{t("media.files.tabVideos")}</TabsTrigger>
            </TabsList>
          </Tabs>
          <div className="relative w-full sm:w-80">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              className="h-8 pl-9 text-xs"
              value={search}
              onChange={(event) => { setSearch(event.target.value); setPage(1); }}
              placeholder={t(tab === "images" ? "media.images.search" : "media.videos.search")}
              aria-label={t(tab === "images" ? "media.images.search" : "media.videos.search")}
            />
          </div>
        </div>

        {activeQuery.isError ? <ErrorState message={activeQuery.error.message} onRetry={() => void activeQuery.refetch()} /> : null}
        {activeQuery.isPending ? <LoadingState /> : null}

        {tab === "images" && !imagesQuery.isPending && imagesQuery.data ? (
          imagesQuery.data.items.length === 0
            ? <EmptyState message={t(normalizedSearch ? "media.images.noMatches" : "media.images.empty")} />
            : (
              <>
                <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4">
                  {imagesQuery.data.items.map((asset) => <ImageCard key={asset.id} asset={asset} locale={i18n.language} />)}
                </div>
                <Pagination page={imagesQuery.data.page} pageSize={imagesQuery.data.pageSize} total={imagesQuery.data.total} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} />
              </>
            )
        ) : null}

        {tab === "videos" && !videosQuery.isPending && videosQuery.data ? (
          videosQuery.data.items.length === 0
            ? <EmptyState message={t(normalizedSearch ? "media.videos.noMatches" : "media.videos.empty")} />
            : (
              <>
                <div className="space-y-2">
                  {videosQuery.data.items.map((job) => <VideoRow key={job.id} job={job} locale={i18n.language} />)}
                </div>
                <Pagination page={videosQuery.data.page} pageSize={videosQuery.data.pageSize} total={videosQuery.data.total} onPageChange={setPage} onPageSizeChange={(value) => { setPageSize(value); setPage(1); }} />
              </>
            )
        ) : null}
      </section>
    </div>
  );
}

function ImageCard({ asset, locale }: { asset: MediaAssetDTO; locale: string }) {
  const { t } = useTranslation();
  return (
    <a href={asset.url} target="_blank" rel="noreferrer" className="group overflow-hidden rounded-xl border bg-card shadow-sm transition hover:border-foreground/20">
      <div className="aspect-square bg-muted/40">
        <img src={asset.url} alt={asset.id} className="size-full object-cover" loading="lazy" />
      </div>
      <div className="space-y-1 p-3 text-xs">
        <div className="flex items-center justify-between gap-2">
          <Badge variant="outline" className="font-normal">{asset.mimeType}</Badge>
          <span className="tabular-nums text-muted-foreground">{formatBytes(asset.sizeBytes, locale)}</span>
        </div>
        <div className="truncate text-muted-foreground" title={asset.id}>{asset.id}</div>
        <div className="text-muted-foreground">{formatDateTime(asset.createdAt, locale)}</div>
        <div className="text-[11px] text-muted-foreground">{t("media.files.openOriginal")}</div>
      </div>
    </a>
  );
}

function VideoRow({ job, locale }: { job: MediaJobDTO; locale: string }) {
  const { t } = useTranslation();
  return (
    <div className="rounded-xl border bg-card p-3 text-xs shadow-sm">
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0 flex-1 space-y-1">
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge variant="outline" className="font-normal">{job.status}</Badge>
            <span className="font-medium">{job.model || "—"}</span>
            <span className="text-muted-foreground">{job.seconds ? `${job.seconds}s` : ""} {job.size}</span>
          </div>
          <p className="line-clamp-2 text-muted-foreground">{job.prompt || t("media.videos.emptyPrompt")}</p>
          <div className="flex flex-wrap gap-x-3 gap-y-1 text-muted-foreground">
            <span>{t("media.videos.account")}: {job.accountName || "—"}</span>
            <span>{t("media.videos.clientKey")}: {job.clientKeyName || "—"}</span>
            <span>{formatDateTime(job.createdAt, locale)}</span>
          </div>
          {job.errorMessage ? <p className="text-destructive">{job.errorMessage}</p> : null}
        </div>
        <div className="tabular-nums text-muted-foreground">{job.progress}%</div>
      </div>
    </div>
  );
}

function MediaMetric({ icon: Icon, loading, label, value, detail }: { icon: LucideIcon; loading: boolean; label: string; value: string; detail: string }) {
  return (
    <div className="rounded-xl border bg-card p-3 shadow-sm">
      <div className="flex items-center gap-2 text-xs text-muted-foreground">
        <Icon className="size-3.5" />
        <span>{label}</span>
      </div>
      <div className="mt-2 text-lg font-medium tracking-tight">
        {loading ? <Spinner className="size-4" /> : value}
      </div>
      <p className="mt-1 truncate text-[11px] text-muted-foreground" title={detail}>{detail}</p>
    </div>
  );
}

function formatBytes(value: number, locale: string): string {
  if (!Number.isFinite(value) || value <= 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let size = value;
  let unit = 0;
  while (size >= 1024 && unit < units.length - 1) {
    size /= 1024;
    unit += 1;
  }
  return `${formatNumber(size, locale, unit === 0 ? 0 : 1)} ${units[unit]}`;
}
