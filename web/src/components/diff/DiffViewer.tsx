import { useState, useEffect } from "react";
import { ArrowLeft } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { assetsApi } from "@/lib/api";
import { cn } from "@/lib/utils";
import type { Asset, AssetVersion, DiffResult, DiffLine } from "@/types";

interface DiffViewerProps {
  assetId: string;
  onClose: () => void;
}

export function DiffViewer({ assetId, onClose }: DiffViewerProps) {
  const [asset, setAsset] = useState<Asset | null>(null);
  const [versions, setVersions] = useState<AssetVersion[]>([]);
  const [v1, setV1] = useState<number>(0);
  const [v2, setV2] = useState<number>(0);
  const [diff, setDiff] = useState<DiffResult | null>(null);
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    loadData();
  }, [assetId]);

  useEffect(() => {
    if (v1 > 0 && v2 > 0 && v1 !== v2) {
      loadDiff();
    }
  }, [v1, v2]);

  const loadData = async () => {
    try {
      const [assetData, versionsData] = await Promise.all([
        assetsApi.get(assetId),
        assetsApi.versions(assetId),
      ]);
      setAsset(assetData);
      setVersions(versionsData);

      // Default: compare last two versions
      if (versionsData.length >= 2) {
        setV1(versionsData[1].version);
        setV2(versionsData[0].version);
      }
    } catch (e) {
      console.error("Failed to load data:", e);
    }
  };

  const loadDiff = async () => {
    setLoading(true);
    try {
      const data = await assetsApi.diff(assetId, v1, v2);
      setDiff(data);
    } catch (e) {
      console.error("Failed to load diff:", e);
    } finally {
      setLoading(false);
    }
  };

  if (!asset) {
    return (
      <div className="flex items-center justify-center h-full text-muted-foreground">
        Loading...
      </div>
    );
  }

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="p-3 border-b space-y-3">
        <div className="flex items-center gap-2">
          <Button
            variant="ghost"
            size="icon"
            className="h-7 w-7"
            onClick={onClose}
          >
            <ArrowLeft className="h-4 w-4" />
          </Button>
          <div className="flex-1">
            <div className="font-medium text-sm">{asset.title}</div>
            <div className="text-xs text-muted-foreground">Version Diff</div>
          </div>
        </div>

        {/* Version selectors */}
        <div className="flex items-center gap-2">
          <div className="flex-1">
            <label className="text-xs text-muted-foreground">From</label>
            <Select
              value={String(v1)}
              onValueChange={(val) => setV1(Number(val))}
            >
              <SelectTrigger className="h-8 mt-1">
                <SelectValue placeholder="Select version" />
              </SelectTrigger>
              <SelectContent>
                {versions.map((v) => (
                  <SelectItem key={v.version} value={String(v.version)}>
                    v{v.version}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="pt-4 text-muted-foreground">→</div>
          <div className="flex-1">
            <label className="text-xs text-muted-foreground">To</label>
            <Select
              value={String(v2)}
              onValueChange={(val) => setV2(Number(val))}
            >
              <SelectTrigger className="h-8 mt-1">
                <SelectValue placeholder="Select version" />
              </SelectTrigger>
              <SelectContent>
                {versions.map((v) => (
                  <SelectItem key={v.version} value={String(v.version)}>
                    v{v.version}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        </div>
      </div>

      {/* Diff content */}
      <div className="flex-1 overflow-y-auto">
        {loading ? (
          <div className="flex items-center justify-center h-full text-muted-foreground">
            Loading diff...
          </div>
        ) : diff ? (
          <div className="p-4 font-mono text-sm">
            <div className="flex items-center gap-2 mb-3 text-xs text-muted-foreground">
              <Badge variant="outline" className="text-xs">
                v{diff.version1}
              </Badge>
              →
              <Badge variant="outline" className="text-xs">
                v{diff.version2}
              </Badge>
            </div>
            <div className="border rounded-md overflow-hidden">
              <InlineDiffContent diffs={diff.diffs} />
            </div>
          </div>
        ) : (
          <div className="flex items-center justify-center h-full text-muted-foreground text-sm">
            {versions.length < 2
              ? "Need at least 2 versions to compare"
              : "Select two versions to compare"}
          </div>
        )}
      </div>
    </div>
  );
}

function InlineDiffContent({ diffs }: { diffs: DiffLine[] }) {
  return (
    <>
      {diffs.flatMap((d, i) =>
        d.content.split("\n").map((line, j, arr) => {
          if (j === arr.length - 1 && line === "") return null;
          return (
            <div
              key={`${i}-${j}`}
              className={cn(
                "px-3 py-0.5 text-xs leading-5 whitespace-pre-wrap border-b border-border/30",
                d.type === "insert" && "bg-green-500/10 text-green-700 dark:text-green-400",
                d.type === "delete" && "bg-red-500/10 text-red-700 dark:text-red-400",
                d.type === "equal" && "text-muted-foreground",
              )}
            >
              <span className="inline-block w-4 text-muted-foreground/50 select-none mr-2">
                {d.type === "insert" ? "+" : d.type === "delete" ? "-" : " "}
              </span>
              {line || " "}
            </div>
          );
        }),
      )}
    </>
  );
}
