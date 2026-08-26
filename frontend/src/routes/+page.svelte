<script lang="ts">
	import { Button } from "$lib/components/ui/button";
	import { Input } from "$lib/components/ui/input";
	import * as Card from "$lib/components/ui/card";
	import { Badge } from "$lib/components/ui/badge";
	import { Separator } from "$lib/components/ui/separator";
	import * as Table from "$lib/components/ui/table";
	import { Skeleton } from "$lib/components/ui/skeleton";
	import { Toaster } from "$lib/components/ui/sonner";
	import { toast } from "svelte-sonner";
	import SearchIcon from "@lucide/svelte/icons/search";
	import CopyIcon from "@lucide/svelte/icons/copy";
	import DownloadIcon from "@lucide/svelte/icons/download";
	import ExternalLinkIcon from "@lucide/svelte/icons/external-link";
	import MoonIcon from "@lucide/svelte/icons/moon";
	import SunIcon from "@lucide/svelte/icons/sun";
	import DatabaseIcon from "@lucide/svelte/icons/database";
	import ShieldIcon from "@lucide/svelte/icons/shield";
	import CodeIcon from "@lucide/svelte/icons/code";
	import BotIcon from "@lucide/svelte/icons/bot";
	import TerminalIcon from "@lucide/svelte/icons/terminal";
	import { onMount } from "svelte";

	// API base: use relative in dev (proxied), override via VITE_API_URL if set
	const API_BASE = (import.meta.env.VITE_API_URL as string) || "";

	let apex = $state("");
	let results = $state<{ sub: string; first_seen?: string }[]>([]);
	let meta = $state<{ apex: string; count: number } | null>(null);
	let loading = $state(false);
	let error = $state<string | null>(null);
	let rateRemaining = $state<number | null>(null);
	let showDates = $state(true);
	let orderNew = $state(false);
	let formatJson = $state(false);

	// stats
	let subdomains = $state(0);
	let displayCount = $state(0);
	let recent = $state<{ apex: string; sub: string; first_seen: string }[]>([]);
	let feedQueue: typeof recent = [];
	let seenKeys = new Set<string>();
	let draining = false;
	let queueTimer: number | null = null;

	let theme = $state<"light" | "dark">("dark");

	function applyTheme(t: "light" | "dark", persist = true) {
		theme = t;
		document.documentElement.setAttribute("data-theme", t);
		document.documentElement.style.colorScheme = t;
		document.documentElement.classList.toggle("dark", t === "dark");
		const metaEl = document.getElementById("theme-color");
		if (metaEl) metaEl.setAttribute("content", t === "light" ? "#fbfbfa" : "#09090b");
		try {
			if (persist) {
				const sys = window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
				if (t === sys) localStorage.removeItem("crt-theme");
				else localStorage.setItem("crt-theme", t);
			}
		} catch {}
	}

	function toggleTheme() {
		applyTheme(theme === "light" ? "dark" : "light", true);
	}

	function fmt(n: number) {
		return n.toLocaleString("en-US");
	}

	function rollTo(target: number) {
		const reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
		if (reduce || target <= displayCount) {
			displayCount = target;
			return;
		}
		const from = displayCount;
		const delta = target - from;
		const dur = 850;
		const t0 = performance.now();
		const el = document.getElementById("count");
		el?.classList.add("tick");
		const step = (now: number) => {
			const p = Math.min(1, (now - t0) / dur);
			const e = 1 - Math.pow(1 - p, 3);
			displayCount = Math.round(from + delta * e);
			if (p < 1) requestAnimationFrame(step);
			else el?.classList.remove("tick");
		};
		requestAnimationFrame(step);
	}

	function ingest(next: typeof recent) {
		for (const ev of next) {
			const k = ev.apex + "|" + ev.sub;
			if (seenKeys.has(k)) continue;
			seenKeys.add(k);
			feedQueue.push(ev);
		}
		if (feedQueue.length > 40) feedQueue.splice(0, feedQueue.length - 40);
		if (!draining) reveal();
	}

	function reveal() {
		if (!feedQueue.length) {
			draining = false;
			return;
		}
		draining = true;
		const ev = feedQueue.shift()!;
		recent = [ev, ...recent].slice(0, 8);
		queueTimer = window.setTimeout(reveal, Math.min(700, 3500 / Math.max(feedQueue.length, 1)));
	}

	async function pollStats() {
		try {
			const r = await fetch(`${API_BASE}/v1/stats`, { cache: "no-store" });
			if (!r.ok) return;
			const d = await r.json();
			if (typeof d.subdomains === "number") {
				subdomains = d.subdomains;
				rollTo(d.subdomains);
			}
			if (Array.isArray(d.recent)) ingest(d.recent);
		} catch {}
	}

	async function doSearch(e?: Event) {
		e?.preventDefault();
		const q = apex.trim().toLowerCase();
		if (!q) {
			error = "missing apex parameter";
			return;
		}
		loading = true;
		error = null;
		results = [];
		meta = null;
		try {
			const params = new URLSearchParams({ apex: q });
			if (formatJson) params.set("format", "json");
			if (showDates) params.set("dates", "1");
			if (orderNew) params.set("order_new", "1");
			const r = await fetch(`${API_BASE}/v1/search?${params.toString()}`);
			const remaining = r.headers.get("x-ratelimit-remaining");
			const limit = r.headers.get("x-ratelimit-limit");
			if (remaining) rateRemaining = parseInt(remaining, 10);
			if (!r.ok) {
				const text = await r.text();
				throw new Error(text.trim() || `request failed: ${r.status}`);
			}
			if (formatJson) {
				const data = await r.json();
				// data is either [{sub,...}] or maybe empty array
				results = data;
				meta = { apex: q, count: data.length };
			} else {
				const text = await r.text();
				const lines = text.split("\n").filter(Boolean);
				results = lines.map((line) => {
					if (showDates && line.includes("\t")) {
						const [sub, first_seen] = line.split("\t");
						return { sub, first_seen };
					}
					return { sub: line };
				});
				meta = { apex: q, count: results.length };
			}
			// update URL hash for sharing without navigation
			history.replaceState(null, "", `#search=${encodeURIComponent(q)}`);
		} catch (err: any) {
			error = err.message || "unknown error";
		} finally {
			loading = false;
		}
	}

	function copyAll() {
		const text = results.map((r) => (showDates && r.first_seen ? `${r.sub}\t${r.first_seen}` : r.sub)).join("\n");
		navigator.clipboard.writeText(text);
		toast.success("Copied to clipboard", { description: `${results.length} names` });
	}

	function downloadTxt() {
		const text = results.map((r) => (showDates && r.first_seen ? `${r.sub}\t${r.first_seen}` : r.sub)).join("\n");
		const blob = new Blob([text], { type: "text/plain" });
		const url = URL.createObjectURL(blob);
		const a = document.createElement("a");
		a.href = url;
		a.download = `${meta?.apex || "subdomains"}.txt`;
		a.click();
		URL.revokeObjectURL(url);
	}

	onMount(() => {
		// theme bootstrap
		try {
			const stored = localStorage.getItem("crt-theme");
			const sys = window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
			const initial = stored === "light" || stored === "dark" ? (stored as any) : sys;
			applyTheme(initial, false);
		} catch {
			applyTheme("dark", false);
		}
		// system listener
		const mq = window.matchMedia("(prefers-color-scheme: light)");
		const handler = () => {
			try {
				if (!localStorage.getItem("crt-theme")) applyTheme(mq.matches ? "light" : "dark", false);
			} catch {}
		};
		mq.addEventListener?.("change", handler);

		// stats poll
		pollStats();
		const iv = setInterval(pollStats, 4000);
		const onVis = () => {
			if (!document.hidden) pollStats();
		};
		document.addEventListener("visibilitychange", onVis);

		// hash restore
		const hash = location.hash;
		const m = hash.match(/search=([^&]+)/);
		if (m) {
			apex = decodeURIComponent(m[1]);
			doSearch();
		}
		// initial count placeholder from DOM if needed
		const el = document.getElementById("count");
		if (el) {
			const n = parseInt(el.textContent?.replace(/[^0-9]/g, "") || "0", 10);
			if (n) {
				subdomains = n;
				displayCount = n;
			}
		}

		return () => {
			clearInterval(iv);
			document.removeEventListener("visibilitychange", onVis);
			mq.removeEventListener?.("change", handler);
			if (queueTimer) clearTimeout(queueTimer);
		};
	});
</script>

<svelte:head>
	<title>crt.name: passive subdomain index</title>
	<meta name="description" content="A passive subdomain index fed by the public Certificate Transparency firehose. Send an apex, get every subdomain this index has on file." />
	<meta name="theme-color" content="#09090b" id="theme-color" />
	<link rel="preconnect" href="https://fonts.googleapis.com" />
	<link rel="preconnect" href="https://fonts.gstatic.com" crossorigin="" />
	<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet" />
</svelte:head>

<Toaster position="top-right" />

<div class="min-h-screen bg-background text-foreground selection:bg-primary selection:text-primary-foreground">
	<!-- subtle grid backdrop -->
	<div class="pointer-events-none fixed inset-0 -z-10 bg-[linear-gradient(to_right,#8080800a_1px,transparent_1px),linear-gradient(to_bottom,#8080800a_1px,transparent_1px)] bg-[size:24px_24px]"></div>
	<div class="pointer-events-none fixed inset-0 -z-10 bg-gradient-to-b from-background via-background to-background/0"></div>

	<header class="sticky top-0 z-40 border-b bg-background/80 backdrop-blur supports-[backdrop-filter]:bg-background/60">
		<div class="mx-auto flex h-[52px] max-w-[980px] items-center justify-between gap-4 px-4 sm:px-6">
			<a href="#top" class="flex items-center gap-2 font-mono text-[13px] font-medium tracking-tight">
				<span class="inline-flex size-6 items-center justify-center rounded-md bg-primary text-primary-foreground font-mono text-[11px]">◧</span>
				<span class="tracking-[-0.02em]">crt.name</span>
				<span class="hidden text-muted-foreground sm:inline">— passive CT index</span>
			</a>
			<nav class="hidden items-center gap-1 text-sm sm:flex">
				<a href="#index" class="rounded-md px-2.5 py-1.5 hover:bg-muted">Index</a>
				<a href="#api" class="rounded-md px-2.5 py-1.5 hover:bg-muted">API</a>
				<a href="#mcp" class="rounded-md px-2.5 py-1.5 hover:bg-muted">MCP</a>
				<a href="#cli" class="rounded-md px-2.5 py-1.5 hover:bg-muted">CLI</a>
				<a href="mailto:melvin@outbox.lu" class="rounded-md px-2.5 py-1.5 text-muted-foreground hover:bg-muted hover:text-foreground">Contact</a>
			</nav>
			<div class="flex items-center gap-1">
				<Button variant="ghost" size="icon" aria-label="Toggle theme" onclick={toggleTheme} class="theme-toggle size-8">
					{#if theme === "dark"}
						<SunIcon data-icon="inline-start" />
					{:else}
						<MoonIcon data-icon="inline-start" />
					{/if}
				</Button>
				<a href="https://github.com/melvinsh/subfaster" target="_blank" rel="noreferrer" class="hidden sm:inline-flex">
					<Button variant="outline" size="sm" class="h-8 gap-1.5">
						GitHub <ExternalLinkIcon data-icon="inline-end" />
					</Button>
				</a>
			</div>
		</div>
	</header>

	<main class="mx-auto max-w-[980px] px-4 pb-16 sm:px-6">
		<!-- Hero -->
		<section id="top" class="pt-10 sm:pt-14">
			<div class="grid gap-8 lg:grid-cols-[1.15fr_0.85fr] lg:items-start">
				<div>
					<div class="inline-flex items-center gap-2 rounded-full border bg-card px-3 py-1 text-xs text-muted-foreground shadow-sm">
						<span class="size-1.5 animate-pulse rounded-full bg-emerald-500"></span>
						live from the CT firehose
						<Separator orientation="vertical" class="mx-1 h-3" />
						<span class="font-mono text-[11px]">100 req/IP/day · no token</span>
					</div>
					<h1 class="mt-4 font-[Inter] text-[32px] font-semibold leading-[0.95] tracking-[-0.03em] sm:text-[42px]">
						Search the subdomain index.
					</h1>
					<p class="mt-3 max-w-[52ch] text-[15px] leading-6 text-muted-foreground">
						Give it an apex domain and you get every subdomain on file, plus the date each name first showed up.
					</p>

					<form class="apex-search mt-6 flex max-w-[520px] items-center gap-2 rounded-xl border bg-card p-1.5 shadow-sm focus-within:ring-2 focus-within:ring-ring" onsubmit={doSearch}>
						<label class="sr-only" for="apex">Apex domain</label>
						<Input
							id="apex"
							bind:value={apex}
							placeholder="namecheap.com"
							autocomplete="off"
							autocapitalize="off"
							spellcheck={false}
							inputmode="url"
							class="h-9 flex-1 border-0 bg-transparent shadow-none focus-visible:ring-0 font-mono text-[14px]"
						/>
						<Button type="submit" class="h-9 gap-1.5 rounded-lg px-4 font-medium" disabled={loading}>
							{#if loading}
								<span class="size-3 animate-spin rounded-full border-2 border-current border-t-transparent"></span>
								Searching
							{:else}
								Search <SearchIcon data-icon="inline-end" />
							{/if}
						</Button>
					</form>
					<p class="mt-2 max-w-[520px] text-xs text-muted-foreground">
						Free · no token · example:
						<button class="underline decoration-dotted underline-offset-2 hover:text-foreground" onclick={() => { apex = "namecheap.com"; doSearch(); }}>namecheap.com</button>,
						<button class="underline decoration-dotted underline-offset-2 hover:text-foreground" onclick={() => { apex = "example.com"; doSearch(); }}>example.com</button>,
						<button class="underline decoration-dotted underline-offset-2 hover:text-foreground" onclick={() => { apex = "google.com"; doSearch(); }}>google.com</button>
					</p>

					{#if error}
						<div class="mt-4 max-w-[520px] rounded-lg border border-destructive/50 bg-destructive/10 px-3 py-2 text-sm text-destructive">
							<span class="font-mono text-xs">error:</span> {error}
						</div>
					{/if}

					<div class="mt-4 flex items-center gap-2 text-xs">
						{#if rateRemaining !== null}
							<Badge variant="secondary" class="font-mono">rate: {rateRemaining}/100 left</Badge>
						{/if}
						<span class="text-muted-foreground">Format:</span>
						<button class="rounded border px-2 py-0.5 text-xs font-medium {formatJson ? 'bg-primary text-primary-foreground' : 'bg-card'}" onclick={() => (formatJson = !formatJson)}>{formatJson ? "JSON" : "text"}</button>
						<button class="rounded border px-2 py-0.5 text-xs font-medium {showDates ? 'bg-primary text-primary-foreground' : 'bg-card'}" onclick={() => (showDates = !showDates)}>dates {showDates ? "on" : "off"}</button>
						<button class="rounded border px-2 py-0.5 text-xs font-medium {orderNew ? 'bg-primary text-primary-foreground' : 'bg-card'}" onclick={() => (orderNew = !orderNew)}>{orderNew ? "newest first" : "oldest first"}</button>
					</div>
				</div>

				<!-- Live counter + feed -->
				<div class="space-y-4">
					<Card.Root class="overflow-hidden border shadow-sm">
						<Card.Header class="pb-2">
							<Card.Description class="flex items-center gap-2 text-xs">
								<DatabaseIcon data-icon="inline-start" /> indexed
							</Card.Description>
							<Card.Title class="font-mono text-[34px] font-semibold tracking-[-0.03em] leading-none sm:text-[40px]">
								<span id="count" class="tabular-nums tracking-tight">{fmt(displayCount || subdomains)}</span>
							</Card.Title>
							<p class="text-xs text-muted-foreground">apexes and subdomains · <span class="font-mono">live</span> polling <code class="rounded bg-muted px-1 py-0.5 font-mono text-[11px]">/v1/stats</code> every 4s</p>
						</Card.Header>
						<Separator />
						<Card.Content class="p-0">
							<div class="max-h-[248px] overflow-hidden">
								<ul id="feed" class="divide-y font-mono text-[12.5px] leading-5">
									{#each recent as ev (ev.apex + '|' + ev.sub)}
										<li class="flex items-center justify-between gap-2 px-3 py-1.5 hover:bg-muted/50">
											<span class="host truncate">{ev.sub}</span>
											<span class="shrink-0 text-[11px] text-muted-foreground">{new Date(ev.first_seen).toLocaleDateString()}</span>
										</li>
									{/each}
									{#if recent.length === 0}
										{#each Array(8) as _, i}
											<li class="flex items-center justify-between gap-2 px-3 py-1.5">
												<Skeleton class="h-3 w-[62%]" />
												<Skeleton class="h-3 w-16" />
											</li>
										{/each}
									{/if}
								</ul>
							</div>
							<div class="flex items-center justify-between border-t bg-muted/30 px-3 py-2 text-[11px] text-muted-foreground">
								<span class="font-mono">recent · deduped queue</span>
								<span class="inline-flex items-center gap-1"><span class="size-1 rounded-full bg-emerald-500 animate-pulse"></span> streaming</span>
							</div>
						</Card.Content>
					</Card.Root>
					<div class="grid grid-cols-3 gap-2 text-center text-xs">
						<div class="rounded-lg border bg-card p-2">
							<div class="font-mono text-sm font-medium">Static CT</div>
							<div class="text-muted-foreground">Sycamore / Tuscolo</div>
						</div>
						<div class="rounded-lg border bg-card p-2">
							<div class="font-mono text-sm font-medium">RFC 6962</div>
							<div class="text-muted-foreground">Argon / Nimbus</div>
						</div>
						<div class="rounded-lg border bg-card p-2">
							<div class="font-mono text-sm font-medium">Backfills</div>
							<div class="text-muted-foreground">Crawl · CZDS</div>
						</div>
					</div>
				</div>
			</div>
		</section>

		<!-- Results -->
		{#if meta || loading}
			<section class="mt-8">
				<Card.Root class="overflow-hidden">
					<Card.Header class="flex flex-row items-center justify-between gap-4 space-y-0 py-3">
						<div>
							<Card.Title class="text-sm font-medium">Results for <span class="font-mono">{meta?.apex || apex}</span></Card.Title>
							<Card.Description class="font-mono text-xs">
								{#if loading} Searching… {:else} {meta?.count ?? 0} subdomains · sorted {orderNew ? "newest first" : "oldest first"} {/if}
							</Card.Description>
						</div>
						<div class="flex items-center gap-1.5">
							<Button variant="outline" size="sm" class="h-7 gap-1.5" onclick={copyAll} disabled={!results.length}>
								<CopyIcon data-icon="inline-start" /> Copy
							</Button>
							<Button variant="outline" size="sm" class="h-7 gap-1.5" onclick={downloadTxt} disabled={!results.length}>
								<DownloadIcon data-icon="inline-start" /> .txt
							</Button>
							<a href={`${API_BASE}/v1/search?apex=${encodeURIComponent(meta?.apex || apex)}${formatJson ? "&format=json" : ""}${showDates ? "&dates=1" : ""}${orderNew ? "&order_new=1" : ""}`} target="_blank" rel="noreferrer">
								<Button variant="ghost" size="sm" class="h-7 gap-1"><ExternalLinkIcon data-icon="inline-start" /> raw</Button>
							</a>
						</div>
					</Card.Header>
					<Separator />
					<Card.Content class="p-0">
						{#if loading}
							<div class="space-y-2 p-4">
								{#each Array(8) as _}
									<Skeleton class="h-4 w-full" />
								{/each}
							</div>
						{:else if !results.length}
							<div class="p-8 text-center text-sm text-muted-foreground">
								No subdomains on file for <span class="font-mono text-foreground">{meta?.apex}</span>. Try another apex.
							</div>
						{:else if formatJson}
							<pre class="max-h-[420px] overflow-auto bg-muted/20 p-3 font-mono text-xs leading-5"><code>{JSON.stringify(results, null, 2)}</code></pre>
						{:else}
							<div class="max-h-[420px] overflow-auto">
								<Table.Root>
									<Table.Header class="sticky top-0 bg-card">
										<Table.Row>
											<Table.Head class="font-mono text-xs">subdomain</Table.Head>
											{#if showDates}
												<Table.Head class="w-[160px] font-mono text-xs text-right">first seen</Table.Head>
											{/if}
										</Table.Row>
									</Table.Header>
									<Table.Body class="font-mono text-[13px]">
										{#each results as r}
											<Table.Row class="hover:bg-muted/40">
												<Table.Cell class="py-1.5 truncate">{r.sub}</Table.Cell>
												{#if showDates}
													<Table.Cell class="py-1.5 text-right text-xs tabular-nums text-muted-foreground">{r.first_seen ? new Date(r.first_seen).toISOString().slice(0, 19).replace("T", " ") + "Z" : "—"}</Table.Cell>
												{/if}
											</Table.Row>
										{/each}
									</Table.Body>
								</Table.Root>
							</div>
						{/if}
					</Card.Content>
					{#if results.length}
						<div class="border-t bg-muted/20 px-3 py-2 font-mono text-[11px] text-muted-foreground">
							{results.length} rows · share as
							<code class="rounded bg-background px-1 py-0.5">GET {API_BASE}/v1/search?apex={meta?.apex}&format={formatJson ? "json" : "text"}{showDates ? "&dates=1" : ""}</code>
						</div>
					{/if}
				</Card.Root>
			</section>
		{/if}

		<Separator class="my-10" />

		<!-- Index -->
		<section id="index" class="scroll-mt-16">
			<div class="flex items-center gap-2">
				<ShieldIcon data-icon="inline-start" />
				<h2 class="text-[18px] font-semibold tracking-tight">How the index is built.</h2>
			</div>
			<div class="mt-3 grid gap-6 text-sm leading-6 text-muted-foreground lg:grid-cols-[1.2fr_0.8fr]">
				<div class="space-y-3">
					<p>
						The index reads every usable <strong class="text-foreground">CT log</strong> in Chrome's and Apple's programs as entries land: Static CT (Let's Encrypt Sunlight, Geomys, Cloudflare Raio) and legacy RFC 6962 (Google Argon, Sectigo, DigiCert). It parses each certificate in memory, pulls out the SAN hostnames, then throws the rest away.
					</p>
					<p>That only covers new entries. Older names come from retired CT logs replayed from the <strong class="text-foreground">Internet Archive</strong>, plus <strong class="text-foreground">Common Crawl</strong>, <strong class="text-foreground">ICANN CZDS</strong> zone files, <strong class="text-foreground">ProjectDiscovery Chaos</strong>, and <strong class="text-foreground">HaGeZi</strong> DNS blocklists.</p>
					<p>A prober also works through common hostnames against known apexes on a loop, and anything it turns up joins the same index.</p>
					<p class="font-mono text-xs">The index normalizes every name by apex and keeps three fields: <code class="rounded bg-muted px-1 py-0.5">(apex, subdomain, first-seen)</code>. It holds no certificate metadata and never deletes a name.</p>
				</div>
				<Card.Root class="bg-muted/30">
					<Card.Header class="pb-3">
						<Card.Title class="flex items-center gap-2 text-sm"><DatabaseIcon data-icon="inline-start" /> Sources</Card.Title>
					</Card.Header>
					<Card.Content class="space-y-2 text-xs leading-5">
						<div class="flex items-center justify-between"><span>Static CT shards</span><Badge variant="secondary" class="font-mono">Sycamore / Tuscolo</Badge></div>
						<div class="flex items-center justify-between"><span>RFC 6962 logs</span><Badge variant="secondary" class="font-mono">Argon / Nimbus</Badge></div>
						<div class="flex items-center justify-between"><span>Common Crawl + CZDS</span><Badge variant="outline" class="font-mono">backfill</Badge></div>
						<div class="flex items-center justify-between"><span>Chaos + HaGeZi</span><Badge variant="outline" class="font-mono">backfill</Badge></div>
						<div class="flex items-center justify-between"><span>DNS prober</span><Badge variant="outline" class="font-mono">active</Badge></div>
						<Separator class="my-2" />
						<p class="text-muted-foreground">Ingest pipeline: <code class="font-mono">chan → batch INSERT OR IGNORE every 2k rows / 500ms</code>. First-seen is earliest observed timestamp.</p>
					</Card.Content>
				</Card.Root>
			</div>
		</section>

		<!-- API -->
		<section id="api" class="mt-10 scroll-mt-16">
			<div class="flex items-center gap-2">
				<CodeIcon data-icon="inline-start" />
				<h2 class="text-[18px] font-semibold tracking-tight">Query it directly.</h2>
			</div>
			<div class="mt-3 grid gap-4 lg:grid-cols-2">
				<Card.Root>
					<Card.Header>
						<Card.Title class="font-mono text-sm">GET /v1/search?apex=namecheap.com</Card.Title>
						<Card.Description>free · no token · 100 req/IP/day</Card.Description>
					</Card.Header>
					<Card.Content class="space-y-3 text-sm">
						<p class="text-muted-foreground">No token. Returns one subdomain per line by default. <code class="rounded bg-muted px-1 py-0.5 font-mono text-xs">apex</code> must be an eTLD+1; use <code class="rounded bg-muted px-1 py-0.5 font-mono text-xs">?format=json</code> for JSON or <code class="rounded bg-muted px-1 py-0.5 font-mono text-xs">&dates=1</code> to include first-seen dates.</p>
						<pre class="overflow-auto rounded-lg bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-100"><code>curl "{API_BASE}/v1/search?apex=namecheap.com"{"\n"}# → namecheap.com{"\n"}#   developer.namecheap.com{"\n"}#   api.namecheap.com

curl "{API_BASE}/v1/search?apex=namecheap.com&format=json&dates=1" | jq .</code></pre>
						<div class="flex gap-2">
							<Badge variant="secondary" class="font-mono">200 text/plain</Badge>
							<Badge variant="outline" class="font-mono">x-ratelimit-remaining</Badge>
						</div>
					</Card.Content>
				</Card.Root>
				<div class="space-y-4">
					<Card.Root class="border-dashed">
						<Card.Header class="py-3">
							<Card.Title class="font-mono text-xs">GET /v1/find?q=login <Badge variant="outline" class="ml-2 font-mono text-[10px]">beta · bearer</Badge></Card.Title>
						</Card.Header>
						<Card.Content class="font-mono text-xs text-muted-foreground">Search all indexed names when you know part of a subdomain.</Card.Content>
					</Card.Root>
					<Card.Root class="border-dashed">
						<Card.Header class="py-3">
							<Card.Title class="font-mono text-xs">GET /v1/top?n=100 <Badge variant="outline" class="ml-2 font-mono text-[10px]">beta · bearer</Badge></Card.Title>
						</Card.Header>
						<Card.Content class="font-mono text-xs text-muted-foreground">Apexes ranked by how many subdomains the index holds.</Card.Content>
					</Card.Root>
					<Card.Root class="border-dashed">
						<Card.Header class="py-3">
							<Card.Title class="font-mono text-xs">GET /v1/stream <Badge variant="outline" class="ml-2 font-mono text-[10px]">beta · SSE</Badge></Card.Title>
						</Card.Header>
						<Card.Content class="font-mono text-xs text-muted-foreground">Server-sent events for newly indexed names.</Card.Content>
					</Card.Root>
					<p class="text-xs text-muted-foreground">Token access (higher limits and the endpoints above) is in closed beta. Paid plans are not available yet.</p>
				</div>
			</div>
		</section>

		<!-- MCP -->
		<section id="mcp" class="mt-10 scroll-mt-16">
			<div class="flex items-center gap-2">
				<BotIcon data-icon="inline-start" />
				<h2 class="text-[18px] font-semibold tracking-tight">For agent clients.</h2>
			</div>
			<Card.Root class="mt-3">
				<Card.Header>
					<Card.Title class="font-mono text-sm">POST /mcp <Badge variant="secondary" class="ml-2 font-mono">free · no token</Badge></Card.Title>
					<Card.Description>Streamable HTTP endpoint for agent clients. Shares the free <code class="font-mono">/v1/search</code> budget.</Card.Description>
				</Card.Header>
				<Card.Content class="space-y-3">
					<pre class="overflow-auto rounded-lg bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-100"><code>{`curl -X POST ${API_BASE}/mcp \\
  -H "Content-Type: application/json" \\
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"search","arguments":{"apex":"example.com","include_dates":true}}}'`}</code></pre>
					<p class="text-sm text-muted-foreground">One tool, <code class="rounded bg-muted px-1 py-0.5 font-mono text-xs">search</code>: pass an apex, get every indexed subdomain. No token; free tier does not probe.</p>
				</Card.Content>
			</Card.Root>
		</section>

		<!-- CLI -->
		<section id="cli" class="mt-10 scroll-mt-16">
			<div class="flex items-center gap-2">
				<TerminalIcon data-icon="inline-start" />
				<h2 class="text-[18px] font-semibold tracking-tight">On by default in subfaster.</h2>
			</div>
			<Card.Root class="mt-3 bg-zinc-950 text-zinc-100 dark:bg-zinc-950">
				<Card.Content class="p-4 font-mono text-sm leading-6">
					<div class="text-zinc-400"># subdomain enumerator that queries this index directly</div>
					<div>github.com/melvinsh/subfaster <span class="text-zinc-500">— crt source · on by default</span></div>
					<div class="mt-2 rounded bg-zinc-900 px-3 py-2 text-zinc-100">subfaster -d namecheap.com</div>
					<div class="mt-1 text-xs text-zinc-500">its <code>crt</code> source runs by default and takes no key, so the command above reaches the free API on a fresh install.</div>
				</Card.Content>
			</Card.Root>
		</section>

		<footer class="mt-12 flex flex-col items-center justify-between gap-3 border-t pt-6 text-xs text-muted-foreground sm:flex-row">
			<div class="flex items-center gap-4">
				<a href="#top" class="font-mono hover:text-foreground">crt.name</a>
				<a href="https://x.com/melvinszc" target="_blank" rel="noreferrer" class="hover:text-foreground">@melvinszc</a>
				<span class="hidden sm:inline">· clone built with SvelteKit + Go</span>
			</div>
			<div class="font-mono text-[11px]">CT is public. Be polite: identify your UA with an email, respect 512 req/s.</div>
		</footer>
	</main>
</div>

<style>
	:global(.tick) {
		color: hsl(var(--primary));
		transition: color 150ms;
	}
</style>
