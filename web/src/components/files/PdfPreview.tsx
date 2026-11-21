import { useEffect, useState } from "react";
import { Loader2 } from "lucide-react";

export function PdfPreview({ src, path }: { src: string; path: string }) {
  const [loaded, setLoaded] = useState<{ src: string; url: string } | null>(null);
  const [errorSrc, setErrorSrc] = useState<string | null>(null);
  const url = loaded?.src === src ? loaded.url : null;
  const error = errorSrc === src;

  useEffect(() => {
    if (navigator.pdfViewerEnabled === false) return;
    const controller = new AbortController();
    let objectUrl: string | undefined;
    // A blob lets the native reader open the PDF without inheriting the file
    // endpoint's HTML sandbox policy, which disables browser PDF plugins.
    void fetch(src, { signal: controller.signal }).then(async response => {
      if (!response.ok) throw new Error("Unable to load PDF");
      const blob = await response.blob();
      if (controller.signal.aborted) return;
      objectUrl = URL.createObjectURL(new Blob([blob], { type: "application/pdf" }));
      setLoaded({ src, url: objectUrl });
    }).catch(() => {
      if (!controller.signal.aborted) setErrorSrc(src);
    });
    return () => {
      controller.abort();
      if (objectUrl) URL.revokeObjectURL(objectUrl);
    };
  }, [src]);

  if (error || navigator.pdfViewerEnabled === false) {
    return (
      <div role="alert" className="flex-1 flex items-center justify-center px-6 text-center text-sm text-muted-foreground">
        {error ? "Unable to load PDF. Try refreshing or download the file." : "PDF preview is unavailable in this browser. Download the file to view it."}
      </div>
    );
  }
  if (!url) {
    return <div role="status" aria-label="Loading PDF" className="flex-1 flex items-center justify-center"><Loader2 className="h-5 w-5 animate-spin text-muted-foreground" /></div>;
  }
  return <iframe src={url} title={`PDF preview: ${path}`} className="flex-1 min-h-0 w-full border-0" />;
}
