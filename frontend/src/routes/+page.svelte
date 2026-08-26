<script lang="ts">
	import { Button } from "$lib/components/ui/button";
	import { Input } from "$lib/components/ui/input";
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
	import { onMount } from "svelte";

	const API_BASE = (import.meta.env.VITE_API_URL as string) || "";

	let apex = $state("");
	let results = $state<{ sub: string; first_seen?: string }[]>([]);
	let meta = $state<{ apex: string; count: number } | null>(null);
	let loading = $state(false);
	let error = $state<string | null>(null);
	let rateRemaining = $state<number | null>(null);
	let rateLimit = $state<number | null>(null);
	let showDates = $state(true);
	let orderNew = $state(false);
	let formatJson = $state(false);

	let subdomains = $state(0);
	let displayCount = $state(0);
	let recent = $state<{ apex: string; sub: string; first_seen: string }[]>([]);
	let progressLogs = $state<{ name: string; percent?: number; behind?: number }[]>([]);
	let feedQueue: typeof recent = [];
	let seenKeys = new Set<string>();
	let draining = false;
	let queueTimer: number | null = null;
	let theme = $state<"light" | "dark">("dark");

	function applyTheme(t: "light" | "dark", persist=true){
		theme=t;
		document.documentElement.setAttribute("data-theme",t);
		document.documentElement.style.colorScheme=t;
		document.documentElement.classList.toggle("dark",t==="dark");
		document.getElementById("theme-color")?.setAttribute("content",t==="light"?"#fbfbfa":"#09090b");
		try{ if(persist){ const sys=matchMedia("(prefers-color-scheme: light)").matches?"light":"dark"; if(t===sys) localStorage.removeItem("crt-theme"); else localStorage.setItem("crt-theme",t);} }catch{}
	}
	function toggleTheme(){ applyTheme(theme==="light"?"dark":"light",true); }
	function fmt(n:number){ return n.toLocaleString("en-US"); }
	function rollTo(target:number){
		if(matchMedia("(prefers-reduced-motion: reduce)").matches||target<=displayCount){ displayCount=target; return; }
		const from=displayCount, delta=target-from, dur=850, t0=performance.now();
		document.getElementById("count")?.classList.add("tick");
		const step=(now:number)=>{ const p=Math.min(1,(now-t0)/dur); displayCount=Math.round(from+delta*(1-Math.pow(1-p,3))); if(p<1) requestAnimationFrame(step); else document.getElementById("count")?.classList.remove("tick"); };
		requestAnimationFrame(step);
	}
	function ingest(next: typeof recent){
		for(const ev of next){ const k=ev.apex+"|"+ev.sub; if(seenKeys.has(k)) continue; seenKeys.add(k); feedQueue.push(ev); }
		if(feedQueue.length>40) feedQueue.splice(0,feedQueue.length-40);
		if(!draining) reveal();
	}
	function reveal(){
		if(!feedQueue.length){ draining=false; return; }
		draining=true; recent=[feedQueue.shift()!, ...recent].slice(0,8);
		queueTimer=window.setTimeout(reveal, Math.min(700,3500/Math.max(feedQueue.length,1)));
	}
	async function pollStats(){
		try{ const r=await fetch(`${API_BASE}/v1/stats`,{cache:"no-store"}); if(!r.ok) return; const d=await r.json(); if(typeof d.subdomains==="number"){ subdomains=d.subdomains; rollTo(d.subdomains);} if(Array.isArray(d.recent)) ingest(d.recent);}catch{}
	}
	async function pollProgress(){
		try{ const r=await fetch(`${API_BASE}/v1/progress`,{cache:"no-store"}); if(!r.ok) return; const d=await r.json(); if(Array.isArray(d.logs)) progressLogs=d.logs;}catch{}
	}
	async function doSearch(e?:Event){
		e?.preventDefault();
		const q=apex.trim().toLowerCase(); if(!q){ error="enter an apex — e.g. example.com"; return; }
		loading=true; error=null; results=[]; meta=null;
		try{
			const params=new URLSearchParams({apex:q});
			if(formatJson) params.set("format","json");
			if(showDates) params.set("dates","1");
			if(orderNew) params.set("order_new","1");
			const r=await fetch(`${API_BASE}/v1/search?${params}`);
			const rem=r.headers.get("x-ratelimit-remaining"); const lim=r.headers.get("x-ratelimit-limit");
			if(rem&&lim){ rateRemaining=parseInt(rem,10); rateLimit=parseInt(lim,10); } else { rateRemaining=null; rateLimit=null; }
			if(!r.ok) throw new Error((await r.text()).trim()||`${r.status}`);
			if(formatJson){ const data=await r.json(); results=data; meta={apex:q,count:data.length}; }
			else { const text=await r.text(); results=text.split("\n").filter(Boolean).map(l=> showDates&&l.includes("\t")? (()=>{const [sub,first_seen]=l.split("\t"); return {sub,first_seen}})():{sub:l}); meta={apex:q,count:results.length}; }
			history.replaceState(null,"",`#search=${encodeURIComponent(q)}`);
			// scroll to results
			setTimeout(()=>document.getElementById("results")?.scrollIntoView({behavior:"smooth",block:"start"}),100);
		}catch(err:any){ error=err.message; } finally{ loading=false; }
	}
	function copyAll(){ navigator.clipboard.writeText(results.map(r=> showDates&&r.first_seen? `${r.sub}\t${r.first_seen}`:r.sub).join("\n")); toast.success("Copied",{description:`${results.length} names`}); }
	function downloadTxt(){ const a=document.createElement("a"); a.href=URL.createObjectURL(new Blob([results.map(r=> showDates&&r.first_seen? `${r.sub}\t${r.first_seen}`:r.sub).join("\n")],{type:"text/plain"})); a.download=`${meta?.apex}.txt`; a.click(); }

	let totalBehind=$derived(progressLogs.reduce((a,b)=>a+(b.behind??0),0));
	let avgPercent=$derived(progressLogs.length? progressLogs.reduce((a,b)=>a+(b.percent??0),0)/progressLogs.length : 0);

	onMount(()=>{
		try{ const s=localStorage.getItem("crt-theme"); const sys=matchMedia("(prefers-color-scheme: light)").matches?"light":"dark"; applyTheme(s==="light"||s==="dark"? s as any : sys,false);}catch{ applyTheme("dark",false);}
		const mq=matchMedia("(prefers-color-scheme: light)"); const h=()=>{try{if(!localStorage.getItem("crt-theme")) applyTheme(mq.matches?"light":"dark",false);}catch{}}; mq.addEventListener?.("change",h);
		pollStats(); pollProgress(); const iv=setInterval(pollStats,4000); const iv2=setInterval(pollProgress,8000);
		const onVis=()=>{if(!document.hidden){pollStats(); pollProgress();}}; document.addEventListener("visibilitychange",onVis);
		const m=location.hash.match(/search=([^&]+)/); if(m){ apex=decodeURIComponent(m[1]); doSearch(); }
		return ()=>{ clearInterval(iv); clearInterval(iv2); document.removeEventListener("visibilitychange",onVis); mq.removeEventListener?.("change",h); if(queueTimer) clearTimeout(queueTimer); };
	});
</script>

<svelte:head>
	<title>Open Subdomain Search — self-hosted</title>
	<meta name="description" content="Self-hosted subdomain index from CT. Search an apex, get subdomains + first seen." />
	<meta name="theme-color" content="#09090b" id="theme-color" />
	<link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet" />
</svelte:head>

<Toaster position="top-right" />

<div class="min-h-screen bg-background text-foreground selection:bg-primary selection:text-primary-foreground">
	<div class="pointer-events-none fixed inset-0 -z-10 bg-[linear-gradient(to_right,#8080800a_1px,transparent_1px),linear-gradient(to_bottom,#8080800a_1px,transparent_1px)] bg-[size:32px_32px]"></div>

	<header class="sticky top-0 z-40 border-b bg-background/80 backdrop-blur">
		<div class="mx-auto flex h-12 max-w-[1100px] items-center justify-between px-6">
			<a href="#top" class="font-mono text-xs font-medium tracking-tight">Open Subdomain Search</a>
			<div class="flex items-center gap-3">
				<span class="hidden items-center gap-2 font-mono text-xs text-muted-foreground sm:inline-flex">
					<span class="size-1.5 animate-pulse rounded-full bg-primary"></span> self-hosted
					{#if rateLimit!==null}<span>· {rateRemaining}/{rateLimit}</span>{/if}
				</span>
				<a href="https://github.com" target="_blank" class="font-mono text-xs text-muted-foreground hover:text-foreground">GitHub</a>
				<Button variant="ghost" size="icon" class="size-7" aria-label="Toggle theme" onclick={toggleTheme}>
					{#if theme==="dark"}<SunIcon data-icon="inline-start" />{:else}<MoonIcon data-icon="inline-start" />{/if}
				</Button>
			</div>
		</div>
	</header>

	<main class="mx-auto max-w-[1100px] px-6 pb-16">
		<!-- Hero exactly like original: left text, right stat card -->
		<section class="grid gap-10 py-12 sm:py-16 lg:grid-cols-[1.05fr_0.85fr] lg:items-start">
			<div>
				<h1 class="max-w-[14ch] text-[36px] font-semibold leading-[0.9] tracking-[-0.03em] sm:text-[48px]">Search the subdomain index.</h1>
				<p class="mt-4 max-w-[46ch] text-[14px] leading-6 text-muted-foreground">Give it an apex domain and you get every subdomain on file, plus the date each name first showed up.</p>

				<form class="mt-6 flex max-w-[420px] items-center gap-2" onsubmit={doSearch}>
					<Input id="apex" bind:value={apex} placeholder="google.com" autocomplete="off" spellcheck={false} inputmode="url" class="h-9 flex-1 rounded-full border bg-card px-4 font-mono text-sm" />
					<Button type="submit" class="h-9 rounded-full px-5 font-medium" disabled={loading}>
						{#if loading}<span class="size-3 animate-spin rounded-full border-2 border-current border-t-transparent"></span>{:else}Search <span aria-hidden="true">→</span>{/if}
					</Button>
				</form>
				<p class="mt-2 font-mono text-xs text-muted-foreground">
					{#if rateLimit!==null}Free · no token · 100 requests per IP per day{:else}Self-hosted · no limit · local index{/if}
				</p>
				<div class="mt-3 flex flex-wrap gap-1.5">
					<button class="font-mono text-xs underline decoration-dotted underline-offset-4 hover:text-foreground text-muted-foreground" onclick={()=>{apex="example.com"; doSearch();}}>example.com</button>
					<span class="text-muted-foreground">·</span>
					<button class="font-mono text-xs underline decoration-dotted underline-offset-4 hover:text-foreground text-muted-foreground" onclick={()=>{apex="github.com"; doSearch();}}>github.com</button>
					<span class="text-muted-foreground">·</span>
					<button class="font-mono text-xs underline decoration-dotted underline-offset-4 hover:text-foreground text-muted-foreground" onclick={()=>{apex="stripe.com"; doSearch();}}>stripe.com</button>
				</div>
				{#if error}<p class="mt-4 font-mono text-sm text-destructive">{error}</p>{/if}

				<div class="mt-4 flex gap-2">
					<button class="rounded-full border px-3 py-1 font-mono text-xs {formatJson?'bg-foreground text-background':'bg-card'}" onclick={()=>formatJson=!formatJson}>json</button>
					<button class="rounded-full border px-3 py-1 font-mono text-xs {showDates?'bg-foreground text-background':'bg-card'}" onclick={()=>showDates=!showDates}>dates</button>
					<button class="rounded-full border px-3 py-1 font-mono text-xs {orderNew?'bg-foreground text-background':'bg-card'}" onclick={()=>orderNew=!orderNew}>newest</button>
					{#if rateLimit!==null && rateRemaining!==null}<Badge variant="outline" class="font-mono text-xs">{rateRemaining}/{rateLimit} left</Badge>{/if}
				</div>
			</div>

			<!-- Stat card like original, but with progress -->
			<div class="rounded-2xl border bg-card/50 p-5 backdrop-blur">
				<div class="font-mono text-[34px] font-semibold leading-none tracking-tight sm:text-[36px]" id="count">{fmt(displayCount||subdomains)}</div>
				<div class="mt-1 font-mono text-[11px] tracking-widest text-muted-foreground">APEXES AND SUBDOMAINS INDEXED</div>
				<Separator class="my-3" />
				<div class="flex items-center gap-2 font-mono text-[11px] tracking-widest text-muted-foreground">
					<span class="size-1.5 animate-pulse rounded-full bg-primary"></span> LIVE FROM THE CT FIREHOSE
					{#if progressLogs.length}<span class="ml-auto tabular-nums">{avgPercent.toFixed(2)}% avg · {fmt(totalBehind)} behind</span>{/if}
				</div>
				{#if progressLogs.length}
					<div class="mt-2 space-y-1">
						{#each progressLogs.slice(0,4) as lg}
							<div class="h-1 overflow-hidden rounded-full bg-muted"><div class="h-1 bg-primary transition-all" style:width={`${Math.min(100,lg.percent??0)}%`}></div></div>
						{/each}
					</div>
				{/if}
				<div class="mt-3 space-y-1 font-mono text-xs">
					{#each recent as ev (ev.apex+'|'+ev.sub)}
						<div class="flex items-center justify-between gap-2 truncate rounded-md bg-muted/40 px-2 py-1">
							<span class="truncate">{ev.sub}</span>
							<span class="shrink-0 text-[11px] text-muted-foreground">{new Date(ev.first_seen).toLocaleDateString()}</span>
						</div>
					{:else}
						{#each Array(5) as _}<Skeleton class="h-6 w-full rounded-md" />{/each}
					{/each}
				</div>
				<p class="mt-3 font-mono text-[11px] text-muted-foreground">Self-hosted · <code class="bg-muted px-1">ctindex.db</code> · SQLite WAL · no token needed</p>
			</div>
		</section>

		<!-- Results: minimal like original text, but table -->
		{#if meta || loading}
			<section id="results" class="scroll-mt-20">
				<div class="flex items-center justify-between border-t pt-6">
					<h2 class="font-mono text-xs tracking-widest text-muted-foreground">RESULTS — {meta ? `${fmt(meta.count)} for ${meta.apex}` : "searching"}</h2>
					{#if meta}<div class="flex gap-1.5"><Button variant="outline" size="sm" class="h-7 font-mono text-xs" onclick={copyAll}><CopyIcon data-icon="inline-start" /> copy</Button><Button variant="outline" size="sm" class="h-7 font-mono text-xs" onclick={downloadTxt}><DownloadIcon data-icon="inline-start" /> .txt</Button></div>{/if}
				</div>
				{#if loading}
					<div class="mt-4 space-y-2">{#each Array(6) as _}<Skeleton class="h-4 w-full" />{/each}</div>
				{:else if !results.length}
					<p class="mt-6 font-mono text-sm text-muted-foreground">No names for <span class="text-foreground">{meta?.apex}</span>.</p>
				{:else if formatJson}
					<pre class="mt-4 max-h-[420px] overflow-auto rounded-xl border bg-card p-4 font-mono text-xs">{JSON.stringify(results,null,2)}</pre>
				{:else}
					<div class="mt-4 max-h-105 overflow-y-auto rounded-xl border bg-card">
						<Table.Root>
							<Table.Header class="sticky top-0 z-10 bg-muted/30"><Table.Row><Table.Head class="h-7 font-mono text-[11px] tracking-widest">SUBDOMAIN</Table.Head>{#if showDates}<Table.Head class="h-7 w-40 text-right font-mono text-[11px] tracking-widest">FIRST SEEN</Table.Head>{/if}</Table.Row></Table.Header>
							<Table.Body class="font-mono text-xs">{#each results as r}<Table.Row class="h-8"><Table.Cell class="py-1.5">{r.sub}</Table.Cell>{#if showDates}<Table.Cell class="py-1.5 text-right tabular-nums text-muted-foreground">{r.first_seen? new Date(r.first_seen).toISOString().slice(0,19).replace("T"," ")+"Z":"—"}</Table.Cell>{/if}</Table.Row>{/each}</Table.Body>
						</Table.Root>
					</div>
				{/if}
			</section>
		{/if}

		<Separator class="my-12" />

		<!-- Index: like original, single column -->
		<section id="index" class="mx-auto max-w-[680px] scroll-mt-20">
			<p class="font-mono text-xs tracking-widest text-muted-foreground">INDEX</p>
			<h2 class="mt-2 text-xl font-semibold tracking-tight">How the index is built.</h2>
			<div class="mt-4 space-y-4 text-sm leading-6 text-muted-foreground">
				<p>The index reads every usable <span class="font-mono text-xs text-foreground border px-1">CT log</span> in Chrome's and Apple's programs as entries land: Static CT (Let's Encrypt Sunlight, Geomys, Cloudflare Raio) and legacy RFC 6962 (Google Argon, Sectigo, DigiCert). It parses each certificate in memory, pulls out the SAN hostnames, then throws the rest away.</p>
				<p>That only covers new entries. Older names come from retired CT logs replayed from the <span class="text-foreground">Internet Archive</span>, plus <span class="text-foreground">Common Crawl</span>, <span class="text-foreground">ICANN CZDS</span> zone files, <span class="text-foreground">ProjectDiscovery Chaos</span>, and <span class="text-foreground">HaGeZi</span> DNS blocklists.</p>
				<p>A prober also works through common hostnames against known apexes on a loop, and anything it turns up joins the same index. The index normalizes every name by apex and keeps three fields: <code class="rounded bg-muted px-1 font-mono text-xs">(apex, subdomain, first-seen)</code>. It holds no certificate metadata and never deletes a name.</p>
				{#if progressLogs.length}
					<details class="rounded-xl border bg-card p-3">
						<summary class="cursor-pointer font-mono text-xs">instance progress — {progressLogs.length} logs · {fmt(totalBehind)} behind</summary>
						<div class="mt-3 space-y-2">
							{#each progressLogs as lg}
								<div class="space-y-1"><div class="flex justify-between font-mono text-xs"><span>{lg.name}</span><span class="tabular-nums">{lg.percent?.toFixed(3)}% · {lg.behind?.toLocaleString()} behind</span></div><div class="h-1.5 overflow-hidden rounded-full bg-muted"><div class="h-1.5 bg-primary" style:width={`${lg.percent??0}%`}></div></div></div>
							{/each}
						</div>
					</details>
				{/if}
			</div>
		</section>

		<Separator class="mx-auto my-10 max-w-[680px]" />

		<section id="api" class="mx-auto max-w-[680px] scroll-mt-20">
			<p class="font-mono text-xs tracking-widest text-muted-foreground">API</p>
			<h2 class="mt-2 text-xl font-semibold tracking-tight">Query it directly.</h2>
			<div class="mt-4 rounded-xl border bg-card p-4">
				<div class="flex items-center justify-between"><code class="font-mono text-xs font-medium">GET /v1/search?apex=namecheap.com</code><span class="font-mono text-[11px] text-muted-foreground">FREE · NO TOKEN {#if rateLimit===null}· NO LIMIT{/if}</span></div>
				<p class="mt-2 text-sm text-muted-foreground">No token{#if rateLimit!==null} · {rateLimit} req/IP/day{/if}. Returns one per line. <code class="font-mono text-xs bg-muted px-1">apex</code> must be eTLD+1; <code class="font-mono text-xs bg-muted px-1">?format=json</code> or <code class="font-mono text-xs bg-muted px-1">&dates=1</code>.</p>
				<pre class="mt-3 overflow-auto rounded-lg bg-zinc-950 p-3 font-mono text-xs leading-5 text-zinc-100">curl "{API_BASE}/v1/search?apex=namecheap.com"{"\n"}# → namecheap.com{"\n"}curl "{API_BASE}/v1/search?apex=namecheap.com&format=json&dates=1" | jq</pre>
			</div>
			<div class="mt-3 grid gap-3">
				<div class="rounded-xl border bg-card p-4"><div class="flex justify-between font-mono text-xs"><span>GET /v1/find?q=login</span><span class="text-muted-foreground">CLOSED BETA</span></div><p class="mt-1 text-sm text-muted-foreground">Search all names by substring.</p></div>
				<div class="rounded-xl border bg-card p-4"><div class="flex justify-between font-mono text-xs"><span>GET /v1/top?n=100</span><span class="text-muted-foreground">CLOSED BETA</span></div><p class="mt-1 text-sm text-muted-foreground">Apexes ranked by count.</p></div>
				<div class="rounded-xl border bg-card p-4"><div class="flex justify-between font-mono text-xs"><span>GET /v1/stream</span><span class="text-muted-foreground">CLOSED BETA</span></div><p class="mt-1 text-sm text-muted-foreground">SSE live names.</p></div>
			</div>
			<p class="mt-3 text-center font-mono text-xs text-muted-foreground">Set <code>RATE_LIMIT</code>/<code>CLONE_TOKEN</code> only if you expose publicly.</p>
		</section>

		<Separator class="mx-auto my-10 max-w-[680px]" />

		<section id="mcp" class="mx-auto max-w-[680px] scroll-mt-20">
			<p class="font-mono text-xs tracking-widest text-muted-foreground">MCP</p>
			<h2 class="mt-2 text-xl font-semibold tracking-tight">For agent clients.</h2>
			<div class="mt-4 rounded-xl border bg-card p-4">
				<div class="flex justify-between font-mono text-xs"><span>POST /mcp</span><span class="text-muted-foreground">FREE · NO TOKEN</span></div>
				<p class="mt-2 text-sm text-muted-foreground">Shares <code>/v1/search</code> budget. Tool <code class="font-mono bg-muted px-1">search</code>: pass apex, get subdomains. No token by default.</p>
				<pre class="mt-3 overflow-auto rounded-lg bg-zinc-950 p-3 font-mono text-xs text-zinc-100">curl -X POST {API_BASE}/mcp -H "Content-Type: application/json" -d '{"{"}jsonrpc":"2.0","id":1,"method":"tools/call","params":{"{"}name":"search","arguments":{"{"}apex":"example.com","include_dates":true{"}"}{"}"}{"}"}'</pre>
			</div>
		</section>

		<Separator class="mx-auto my-10 max-w-[680px]" />

		<section id="cli" class="mx-auto max-w-[680px] scroll-mt-20">
			<p class="font-mono text-xs tracking-widest text-muted-foreground">CLI</p>
			<h2 class="mt-2 text-xl font-semibold tracking-tight">On by default in subfaster.</h2>
			<div class="mt-4 rounded-xl border bg-zinc-950 p-4 font-mono text-sm text-zinc-100">
				<div class="text-xs text-zinc-500"># github.com/melvinsh/subfaster — crt source on by default</div>
				<div class="mt-2 rounded bg-zinc-900 px-3 py-2">subfaster -d namecheap.com</div>
			</div>
			<div class="mt-6 flex justify-center"><a href="#top"><Button class="rounded-full px-6">Search a domain →</Button></a></div>
		</section>

		<footer class="mt-16 flex items-center justify-between border-t py-6 font-mono text-xs text-muted-foreground">
			<span>Open Subdomain Search</span><span class="hidden sm:inline">self-hosted · open source · no ads</span><span>@Cufiy</span>
		</footer>
	</main>
</div>

<style>
	:global(.tick){ color: hsl(var(--primary)); }
</style>
