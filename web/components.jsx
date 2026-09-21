// Landing page components
// Aligned with wshoffner.dev design system.

const PROJECT_REPOSITORY = 'https://github.com/TickTockBent/Sopholeth';

const { useState: useStateR, useEffect: useEffectR, useRef: useRefR, useMemo: useMemoR } = React;

// =============================================================================
// Shared glyph atoms
// =============================================================================

function Glyph({ children, color = '#22d3ee' }) {
  return <span style={{ color, marginRight: 6 }}>{children}</span>;
}

function TerminalFrame({ id, label, children, style }) {
  return (
    <section id={id} style={{
      position: 'relative',
      scrollMarginTop: 76,
      border: '1px solid #22d3ee99',
      padding: '28px 28px 24px',
      margin: '28px 0',
      boxShadow: '0 0 8px #22d3ee20',
      background: 'rgba(10,10,15,0.6)',
      ...style
    }}>
      <span style={{
        position: 'absolute', top: -10, left: 16,
        background: '#0a0a0f', padding: '0 8px',
        color: '#22d3ee', fontSize: 11, letterSpacing: '0.14em',
        textTransform: 'uppercase'
      }}>{label}</span>
      {children}
    </section>);

}

// =============================================================================
// Top navigation — WesOS chrome style
// =============================================================================

function SiteNav() {
  return (
    <header style={{
      position: 'fixed', top: 0, left: 0, right: 0, zIndex: 50,
      background: 'rgba(10,10,15,0.9)',
      backdropFilter: 'blur(4px)',
      WebkitBackdropFilter: 'blur(4px)',
      borderBottom: '1px solid #39ff1433',
      fontFamily: 'var(--font-mono)'
    }}>
      <div style={{
        maxWidth: 1280, margin: '0 auto', padding: '0 24px',
        height: 56, display: 'flex', alignItems: 'center', justifyContent: 'space-between'
      }}>
        <a href="#top" style={{
          color: '#39ff14', textShadow: '0 0 10px #39ff1480',
          fontSize: 14, fontWeight: 700, letterSpacing: '0.1em', whiteSpace: 'nowrap'
        }}>
          <span style={{ color: '#22d3ee' }}>$</span>&nbsp;sopholeth
          <span className="nav-status" style={{ color: '#8b949e', marginLeft: 8, letterSpacing: 0, fontSize: 11 }}>
            prelaunch
          </span>
        </a>
        <nav style={{ display: 'flex', gap: 22, alignItems: 'center', fontSize: 12 }}>
          <NavLink href="#architecture">architecture</NavLink>
          <NavLink href="#patterns">patterns</NavLink>
          <NavLink href="#quickstart">quickstart</NavLink>
          <NavLink href="#api">api</NavLink>
          <a href={PROJECT_REPOSITORY} style={{
            color: '#22d3ee', border: '1px solid #22d3ee4d',
            padding: '6px 12px', fontSize: 12, letterSpacing: '0.06em',
            transition: 'all 0.2s'
          }}
          onMouseEnter={(e) => {
            e.currentTarget.style.color = '#39ff14';
            e.currentTarget.style.borderColor = '#39ff1480';
            e.currentTarget.style.boxShadow = '0 0 8px #39ff1430, inset 0 0 8px #39ff1410';
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.color = '#22d3ee';
            e.currentTarget.style.borderColor = '#22d3ee4d';
            e.currentTarget.style.boxShadow = 'none';
          }}>
            
            <span>@</span> github
          </a>
        </nav>
      </div>
    </header>);

}

function NavLink({ href, children }) {
  return (
    <a className="nav-section-link" href={href} style={{
      color: '#8b949e', fontSize: 12,
      letterSpacing: '0.05em', padding: '4px 2px'
    }}
    onMouseEnter={(e) => e.currentTarget.style.color = '#39ff14'}
    onMouseLeave={(e) => e.currentTarget.style.color = '#8b949e'}>
      {children}</a>);

}

// =============================================================================
// Hero — ASCII banner + tagline + boot sequence
// =============================================================================

const BOOT_LINES = [
{ tag: 'BOOT', color: '#39ff14', text: 'initializing ephemeral storage layer...' },
{ tag: 'NET', color: '#22d3ee', text: 'gossip replication · dynamic quorum' },
{ tag: 'SYS', color: '#facc15', text: 'mandatory TTL enforced · min=300s max=86400s' },
{ tag: 'AUTH', color: '#e040fb', text: 'no accounts · opaque values · client encryption' },
{ tag: 'BOOT', color: '#39ff14', text: 'public launch pending · try a private node.' }];


function Hero() {
  const [visibleLines, setVisibleLines] = useStateR(0);

  useEffectR(() => {
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    if (reduced) {setVisibleLines(BOOT_LINES.length);return;}
    let i = 0;
    const t = setInterval(() => {
      i++;
      setVisibleLines(i);
      if (i >= BOOT_LINES.length) clearInterval(t);
    }, 220);
    return () => clearInterval(t);
  }, []);

  return (
    <section id="top" style={{ paddingTop: 112, paddingBottom: 32 }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', alignItems: 'center', gap: 10, marginBottom: 18 }}>
        <span style={{ color: '#22d3ee', fontSize: 12, letterSpacing: '0.14em' }}>
          // SOPHOLETH
        </span>
        <span style={{ flex: 1, height: 1, background: '#22d3ee33' }}></span>
        <LiveDot />
        <span style={{ color: '#8b949e', fontSize: 11, letterSpacing: '0.1em' }}>
          PUBLIC&nbsp;ALPHA&nbsp;PENDING
        </span>
      </div>

      <div style={{
        margin: '0 0 22px', width: '100%', display: 'flex',
        justifyContent: 'center', alignItems: 'center',
        padding: '22px 0', position: 'relative',
      }}>
        <h1 aria-label="Sopholeth" className="wm-glitch" data-text="Sopholeth" style={{
          position: 'relative',
          fontFamily: 'var(--font-mono)',
          fontSize: 'clamp(32px, 7.5vw, 96px)',
          fontWeight: 700,
          letterSpacing: '0.14em',
          lineHeight: 1,
          margin: 0,
          color: '#39ff14',
          textShadow: '0 0 10px #39ff14cc, 0 0 22px #39ff1466',
          animation: 'wm-glitch-shadow 2.2s infinite',
        }}>Sopholeth</h1>

        <style>{`
          @keyframes header-scan {
            0%   { transform: translateX(-120%); }
            100% { transform: translateX(420%); }
          }
          @keyframes wm-glitch-shadow {
            0%, 100% { text-shadow: 0 0 10px #39ff14cc, 0 0 22px #39ff1466; }
            20%      { text-shadow: 3px 0 10px #e040fbcc, -2px 0 6px #39ff1480; }
            40%      { text-shadow: -3px 0 10px #22d3eecc, 2px 0 6px #39ff1480; }
            60%      { text-shadow: 0 0 12px #39ff14ff, 0 0 24px #39ff14aa; }
            80%      { text-shadow: 2px 0 8px #e040fbaa, -2px 0 8px #22d3eeaa; }
          }
          @keyframes wm-glitch-1 {
            0%, 100% { clip-path: inset(0 0 0 0); transform: translate(0,0); }
            20%      { clip-path: inset(18% 0 62% 0); transform: translate(-2px, 0); }
            40%      { clip-path: inset(52% 0 20% 0); transform: translate(2px, 0); }
            60%      { clip-path: inset(8%  0 82% 0); transform: translate(0, 0); }
            80%      { clip-path: inset(80% 0 8%  0); transform: translate(1px, 0); }
          }
          @keyframes wm-glitch-2 {
            0%, 100% { clip-path: inset(0 0 0 0); transform: translate(0,0); }
            20%      { clip-path: inset(60% 0 20% 0); transform: translate(2px, 0); }
            40%      { clip-path: inset(22% 0 52% 0); transform: translate(-2px, 0); }
            60%      { clip-path: inset(78% 0 12% 0); transform: translate(1px, 0); }
            80%      { clip-path: inset(10% 0 78% 0); transform: translate(-1px, 0); }
          }
          .wm-glitch { position: relative; isolation: isolate; }
          .wm-glitch::before,
          .wm-glitch::after {
            content: attr(data-text);
            position: absolute; top: 0; left: 0;
            width: 100%; height: 100%;
            pointer-events: none;
            z-index: -1;
          }
          .wm-glitch::before {
            color: #e040fb;
            text-shadow: 0 0 10px #e040fbcc;
            animation: wm-glitch-1 0.55s infinite steps(1, end);
          }
          .wm-glitch::after {
            color: #22d3ee;
            text-shadow: 0 0 10px #22d3eecc;
            animation: wm-glitch-2 0.48s infinite steps(1, end);
          }
        `}</style>
      </div>

      <p style={{
        color: '#b8c0cc', fontSize: 16, lineHeight: 1.7,
        maxWidth: 680, margin: '0 0 8px'
      }}>
        wisdom through intentional forgetting.
        <br />
        <span style={{ color: '#8b949e' }}>
          SOF-oh-leth. a distributed network for temporary shared state.
          agents leave payloads, other agents pick them up, and the network cleans itself.
          nobody signs a guest book.
        </span>
      </p>

      <div style={{
        marginTop: 28, padding: '16px 18px',
        border: '1px solid #22d3ee33', background: '#0d1117cc',
        fontSize: 13, lineHeight: 1.65, fontFamily: 'var(--font-mono)'
      }}>
        {BOOT_LINES.slice(0, visibleLines).map((l, i) =>
        <div key={i}>
            <span style={{ color: l.color, fontWeight: 700 }}>[{l.tag}]</span>
            <span style={{ color: '#b8c0cc', marginLeft: 10 }}>{l.text}</span>
          </div>
        )}
        {visibleLines < BOOT_LINES.length &&
        <span style={{
          color: '#39ff14', display: 'inline-block', width: 8, height: 14,
          background: '#39ff14', animation: 'cursor-blink 1s step-end infinite',
          verticalAlign: 'text-bottom'
        }}></span>
        }
        {visibleLines >= BOOT_LINES.length &&
        <div style={{ marginTop: 6 }}>
            <span style={{ color: '#22d3ee' }}>guest@network</span>
            <span style={{ color: '#8b949e' }}>:</span>
            <span style={{ color: '#39ff14' }}>~</span>
            <span style={{ color: '#8b949e' }}>$ </span>
            <span style={{ color: '#b8c0cc' }}>NODE_NETWORK=private ./bin/server</span>
            <span style={{
            display: 'inline-block', width: 8, height: 14,
            background: '#39ff14', marginLeft: 4,
            animation: 'cursor-blink 1s step-end infinite',
            verticalAlign: 'text-bottom'
          }}></span>
          </div>
        }
      </div>

      <div style={{ display: 'flex', gap: 14, marginTop: 28, flexWrap: 'wrap' }}>
        <CTAButton primary href="#quickstart">
          <Glyph color="#0a0a0f">&gt;</Glyph>run a node
        </CTAButton>
        <CTAButton href={PROJECT_REPOSITORY}>
          <Glyph>@</Glyph>github
        </CTAButton>
        <CTAButton href={`${PROJECT_REPOSITORY}/blob/main/docs/architecture.md`}>
          <Glyph>#</Glyph>architecture
        </CTAButton>
      </div>
    </section>);

}

function LiveDot() {
  return (
    <span style={{
      display: 'inline-block', width: 8, height: 8, borderRadius: '50%',
      background: '#39ff14', boxShadow: '0 0 8px #39ff14',
      animation: 'pulse-dot 2s ease-in-out infinite'
    }}></span>);

}

function CTAButton({ children, href, primary }) {
  const [hover, setHover] = useStateR(false);
  const base = primary ?
  { color: hover ? '#0a0a0f' : '#39ff14', bg: hover ? '#39ff14' : 'transparent', border: '#39ff14' } :
  { color: hover ? '#39ff14' : '#22d3ee', bg: 'transparent', border: hover ? '#39ff1480' : '#22d3ee99' };
  return (
    <a href={href}
    onMouseEnter={() => setHover(true)}
    onMouseLeave={() => setHover(false)}
    style={{
      color: base.color,
      background: base.bg,
      border: `1px solid ${base.border}`,
      padding: '10px 16px', fontSize: 13, letterSpacing: '0.08em',
      textTransform: 'lowercase',
      fontFamily: 'var(--font-mono)',
      boxShadow: hover && !primary ? '0 0 8px #39ff1430, inset 0 0 8px #39ff1410' : 'none',
      transition: 'all 0.2s'
    }}>{children}</a>);

}

// =============================================================================
// What-it-is / what-it-is-NOT split
// =============================================================================

function WhatItIs() {
  return (
    <div style={{
      display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(320px, 1fr))',
      gap: 20, margin: '36px 0'
    }}>
      <div style={{
        border: '1px solid #39ff1433',
        boxShadow: '0 0 8px #39ff1420, inset 0 0 10px #39ff1408',
        padding: '22px 22px 18px'
      }}>
        <div style={{ color: '#39ff14', fontSize: 11, letterSpacing: '0.14em', marginBottom: 10 }}>
          // IS://
        </div>
        <ul style={{ padding: 0, margin: 0, listStyle: 'none', fontSize: 14, lineHeight: 1.75 }}>
          <LiGood>temporary, replicated, self-cleaning storage</LiGood>
          <LiGood>a dead-drop rendezvous for agents who don't know each other</LiGood>
          <LiGood>general-purpose — "pipe, not grep"</LiGood>
          <LiGood>one Go node · HTTP and MCP interfaces</LiGood>
        </ul>
      </div>
      <div style={{
        border: '1px solid #e040fb4d',
        boxShadow: '0 0 8px #e040fb20, inset 0 0 10px #e040fb08',
        padding: '22px 22px 18px'
      }}>
        <div style={{ color: '#e040fb', fontSize: 11, letterSpacing: '0.14em', marginBottom: 10 }}>
          // IS NOT://
        </div>
        <ul style={{ padding: 0, margin: 0, listStyle: 'none', fontSize: 14, lineHeight: 1.75 }}>
          <LiBad>a persistent database &mdash; values expire locally</LiBad>
          <LiBad>a message queue &mdash; "leave it and hope they check"</LiBad>
          <LiBad>a secrets vault &mdash; no access control, no encryption</LiBad>
          <LiBad>a cache with eviction &mdash; only TTL, never memory pressure</LiBad>
        </ul>
      </div>
    </div>);

}

function LiGood({ children }) {
  return <li style={{ color: '#b8c0cc', paddingLeft: 18, position: 'relative' }}>
    <span style={{ position: 'absolute', left: 0, color: '#39ff14' }}>&gt;</span>
    {children}
  </li>;
}
function LiBad({ children }) {
  return <li style={{ color: '#b8c0cc', paddingLeft: 18, position: 'relative' }}>
    <span style={{ position: 'absolute', left: 0, color: '#e040fb' }}>×</span>
    {children}
  </li>;
}

// =============================================================================
// Architecture — ASCII network diagram
// =============================================================================

function NetworkDiagram() {
  // viewBox: 800 wide × 440 tall. Scales to any container.
  const G = '#39ff14'; // primary / identity
  const C = '#22d3ee'; // structure
  const M = '#e040fb'; // live / interaction
  const MUTED = '#8b949e';
  const BODY = '#b8c0cc';
  const BG = '#0d1117';

  // box helpers
  const Box = ({ x, y, w, h, color, children, labelTop }) =>
  <g>
      <rect x={x} y={y} width={w} height={h}
    fill={BG} stroke={color} strokeWidth="1" />
      {labelTop &&
    <>
          <rect x={x + 10} y={y - 6} width={labelTop.length * 6.2 + 8}
      height="12" fill={BG} />
          <text x={x + 14} y={y + 4}
      fontFamily="var(--font-mono)" fontSize="9"
      fill={C} letterSpacing="1.4">{labelTop}</text>
        </>
    }
      {children}
    </g>;


  return (
    <div style={{
      border: '1px solid #22d3ee33',
      background: '#0a0a0fcc',
      padding: '20px 12px 14px',
      marginBottom: 22
    }}>
      <svg
        viewBox="0 0 800 440"
        width="100%"
        role="img"
        aria-label="Network topology: two agents interacting with a three-node enclave via PUT and GET, with a TTL timeline underneath"
        style={{ display: 'block', maxWidth: '100%', fontFamily: 'var(--font-mono)', height: "572px" }}>
        
        <defs>
          <marker id="arr-cyan" viewBox="0 0 10 10" refX="9" refY="5"
          markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill={C} />
          </marker>
          <marker id="arr-green" viewBox="0 0 10 10" refX="9" refY="5"
          markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill={G} />
          </marker>
          <marker id="arr-magenta" viewBox="0 0 10 10" refX="9" refY="5"
          markerWidth="6" markerHeight="6" orient="auto-start-reverse">
            <path d="M 0 0 L 10 5 L 0 10 z" fill={M} />
          </marker>
          <filter id="glow-g" x="-50%" y="-50%" width="200%" height="200%">
            <feGaussianBlur stdDeviation="2.5" result="b" />
            <feMerge><feMergeNode in="b" /><feMergeNode in="SourceGraphic" /></feMerge>
          </filter>
          <filter id="glow-c" x="-50%" y="-50%" width="200%" height="200%">
            <feGaussianBlur stdDeviation="1.8" result="b" />
            <feMerge><feMergeNode in="b" /><feMergeNode in="SourceGraphic" /></feMerge>
          </filter>
        </defs>

        {/* ===== Agents ===== */}
        <Box x={60} y={30} w={180} h={70} color={G}>
          <text x={150} y={56} textAnchor="middle" fill={G}
          fontSize="14" fontWeight="700" letterSpacing="2"
          filter="url(#glow-g)">AGENT · A</text>
          <text x={150} y={76} textAnchor="middle" fill={MUTED}
          fontSize="11" fontStyle="italic">"leaves it"</text>
          <text x={150} y={92} textAnchor="middle" fill={BODY}
          fontSize="10">— the writer —</text>
        </Box>

        <Box x={560} y={30} w={180} h={70} color={G}>
          <text x={650} y={56} textAnchor="middle" fill={G}
          fontSize="14" fontWeight="700" letterSpacing="2"
          filter="url(#glow-g)">AGENT · B</text>
          <text x={650} y={76} textAnchor="middle" fill={MUTED}
          fontSize="11" fontStyle="italic">"picks it up"</text>
          <text x={650} y={92} textAnchor="middle" fill={BODY}
          fontSize="10">— the reader —</text>
        </Box>

        {/* ===== Agent → cluster arrows ===== */}
        {/* A: PUT */}
        <line x1={150} y1={100} x2={240} y2={180}
        stroke={G} strokeWidth="1.5" markerEnd="url(#arr-green)" />
        <rect x={126} y={118} width={112} height={38}
        fill={BG} stroke={G} strokeOpacity="0.3" />
        <text x={182} y={132} textAnchor="middle" fill={G}
        fontSize="11" fontWeight="700">PUT</text>
        <text x={182} y={146} textAnchor="middle" fill={BODY}
        fontSize="9.5">/v1/data/{'{'}key{'}'}</text>
        <text x={182} y={156} textAnchor="middle" fill={C}
        fontSize="8.5">X-TTL: 300</text>

        {/* B: GET */}
        <line x1={560} y1={180} x2={650} y2={100}
        stroke={M} strokeWidth="1.5" markerEnd="url(#arr-magenta)" />
        <rect x={562} y={118} width={112} height={38}
        fill={BG} stroke={M} strokeOpacity="0.35" />
        <text x={618} y={132} textAnchor="middle" fill={M}
        fontSize="11" fontWeight="700">GET</text>
        <text x={618} y={146} textAnchor="middle" fill={BODY}
        fontSize="9.5">/v1/data/{'{'}key{'}'}</text>
        <text x={618} y={156} textAnchor="middle" fill={MUTED}
        fontSize="8.5">→ 200 "payload"</text>

        {/* ===== Enclave container ===== */}
        <rect x={30} y={180} width={740} height={170}
        fill="none" stroke={C} strokeOpacity="0.55" strokeWidth="1" />
        <rect x={46} y={174} width={132} height={12} fill={BG} />
        <text x={54} y={184} fill={C} fontSize="10"
        letterSpacing="1.6">NETWORK://ENCLAVE</text>

        {/* ===== Three nodes ===== */}
        {[
        { cx: 180, label: 'NODE·01' },
        { cx: 400, label: 'NODE·02' },
        { cx: 620, label: 'NODE·03' }].
        map((n, i) =>
        <g key={n.label}>
            <rect x={n.cx - 52} y={220} width={104} height={66}
          fill={BG} stroke={G} strokeWidth="1.2"
          filter="url(#glow-g)" />
            <text x={n.cx} y={244} textAnchor="middle"
          fill={G} fontSize="12" fontWeight="700" letterSpacing="1.4">
              {n.label}
            </text>
            <line x1={n.cx - 40} y1={252} x2={n.cx + 40} y2={252}
          stroke={C} strokeOpacity="0.4" />
            <text x={n.cx} y={266} textAnchor="middle"
          fill={MUTED} fontSize="8.5" letterSpacing="0.6">
              in-memory · opaque
            </text>
            <text x={n.cx} y={278} textAnchor="middle"
          fill={MUTED} fontSize="8.5" letterSpacing="0.6">
              TTL enforced
            </text>
            {/* liveness dot */}
            <circle cx={n.cx - 42} cy={230} r="2.5" fill={G}>
              <animate attributeName="opacity"
            values="1;0.3;1" dur="2s" begin={`${i * 0.5}s`}
            repeatCount="indefinite" />
            </circle>
          </g>
        )}

        {/* ===== Gossip connectors ===== */}
        {[
        { x1: 232, x2: 348, label: 'gossip', cx: 290 },
        { x1: 452, x2: 568, label: 'gossip', cx: 510 }].
        map((g, i) =>
        <g key={i}>
            <line x1={g.x1} y1={253} x2={g.x2} y2={253}
          stroke={C} strokeWidth="1" strokeDasharray="4 3" />
            <rect x={g.cx - 26} y={245} width={52} height={15}
          fill={BG} />
            <text x={g.cx} y={256} textAnchor="middle"
          fill={C} fontSize="9.5" letterSpacing="1">
              {g.label}
            </text>
            {/* pulse traveling along the line */}
            <circle r="3" fill={G} opacity="0.9">
              <animate attributeName="cx"
            values={`${g.x1};${g.x2}`} dur="2.4s"
            begin={`${i * 1.2}s`} repeatCount="indefinite" />
              <animate attributeName="cy" values="253;253" dur="2.4s"
            begin={`${i * 1.2}s`} repeatCount="indefinite" />
              <animate attributeName="opacity"
            values="0;1;1;0" dur="2.4s"
            begin={`${i * 1.2}s`} repeatCount="indefinite" />
            </circle>
          </g>
        )}
        {/* wrap-around gossip hint: node3 ↔ node1 */}
        <path d="M 620 286 Q 400 340 180 286"
        fill="none" stroke={C} strokeOpacity="0.35"
        strokeWidth="1" strokeDasharray="4 3" />
        <rect x={380} y={322} width={40} height={14} fill={BG} />
        <text x={400} y={332} textAnchor="middle"
        fill={C} fontSize="9" letterSpacing="1" opacity="0.7">gossip</text>

        {/* ===== TTL timeline ===== */}
        <g transform="translate(0, 380)">
          <text x={30} y={0} fill={C} fontSize="10"
          letterSpacing="1.4">// TTL://TIMELINE</text>

          {/* axis */}
          <line x1={60} y1={28} x2={740} y2={28}
          stroke={MUTED} strokeOpacity="0.4" strokeWidth="1" />

          {/* filled portion (while live) */}
          <line x1={60} y1={28} x2={560} y2={28}
          stroke={G} strokeWidth="2" filter="url(#glow-g)" />
          {/* expired portion */}
          <line x1={560} y1={28} x2={740} y2={28}
          stroke={M} strokeWidth="2" strokeDasharray="3 3" />

          {/* ticks */}
          {[
          { x: 60, label: 't = 0', sub: 'PUT · key set', color: G },
          { x: 310, label: 't = +150s', sub: 'GET · still live', color: C },
          { x: 560, label: 't = +300s', sub: 'TTL reached', color: M },
          { x: 720, label: 't = +301s', sub: 'gone · unrecoverable', color: MUTED }].
          map((tk, i) =>
          <g key={i}>
              <line x1={tk.x} y1={24} x2={tk.x} y2={32}
            stroke={tk.color} strokeWidth="1.5" />
              <text x={tk.x} y={48} textAnchor={i === 3 ? 'end' : i === 0 ? 'start' : 'middle'}
            fill={tk.color} fontSize="10" fontWeight="700"
            letterSpacing="0.8">{tk.label}</text>
              <text x={tk.x} y={60} textAnchor={i === 3 ? 'end' : i === 0 ? 'start' : 'middle'}
            fill={MUTED} fontSize="9">{tk.sub}</text>
            </g>
          )}
        </g>
      </svg>
    </div>);

}

function Architecture() {
  return (
    <TerminalFrame id="architecture" label="ARCH://TOPOLOGY">
      <h2 style={{
        color: '#39ff14', fontSize: 22, margin: '0 0 18px',
        letterSpacing: '0.05em', textShadow: '0 0 10px #39ff1480'
      }}>how it works</h2>

      <NetworkDiagram />

      <div style={{
        display: 'flex', flexWrap: 'wrap', gap: '6px 18px',
        padding: '14px 16px',
        border: '1px solid #22d3ee33',
        background: '#0d1117',
        fontSize: 12.5, lineHeight: 1.6, color: '#b8c0cc'
      }}>
        <span><span style={{ color: '#39ff14' }}>~</span> mandatory TTL</span>
        <span style={{ color: '#22d3ee33' }}>│</span>
        <span><span style={{ color: '#22d3ee' }}>@</span> gossip replication, dynamic quorum</span>
        <span style={{ color: '#22d3ee33' }}>│</span>
        <span><span style={{ color: '#39ff14' }}>#</span> content-agnostic nodes</span>
        <span style={{ color: '#22d3ee33' }}>│</span>
        <span><span style={{ color: '#e040fb' }}>$</span> loosely coupled · no catch-up</span>
      </div>

    </TerminalFrame>);

}

function Principle({ glyph, label, children, color = '#39ff14' }) {
  return (
    <div style={{ borderLeft: `2px solid ${color}4d`, paddingLeft: 14 }}>
      <div style={{
        color, fontSize: 12, letterSpacing: '0.14em',
        textTransform: 'uppercase', marginBottom: 6
      }}>
        <span style={{ marginRight: 8 }}>{glyph}</span>{label}
      </div>
      <p style={{ color: '#b8c0cc', fontSize: 13, lineHeight: 1.65, margin: 0 }}>
        {children}
      </p>
    </div>);

}

// =============================================================================
// Patterns — agent usage patterns as terminal cards
// =============================================================================

const PATTERNS = [
{
  name: 'dead drop',
  tag: '[CORE]', tagColor: '#39ff14',
  desc: 'Agent A stores a payload under a derived key. Agent B computes the same key, retrieves, payload self-destructs after TTL.',
  code: `A: PUT /v1/data/hash(task+pair) X-TTL:300\nB: GET /v1/data/hash(task+pair)\n   → 200 "payload"\n   ... 300s later ...\n   → 404`
},
{
  name: 'scratchpad',
  tag: '[STATE]', tagColor: '#22d3ee',
  desc: 'An agent persists intermediate reasoning across multi-step workflows. Store, retrieve, update, let expire.',
  code: `PUT /v1/data/job:42:scratch X-TTL:600\nGET /v1/data/job:42:scratch\nPUT /v1/data/job:42:scratch  # overwrite`
},
{
  name: 'coordination token',
  tag: '[HINT]', tagColor: '#e040fb',
  desc: 'A shared key carries an advisory claim. Concurrent writers can overwrite it; it does not provide an exclusive lock.',
  code: `HEAD /v1/data/claim:resource-9\n   → 200 "claim observed"\n   → 404 "no live claim here"`
},
{
  name: 'heartbeat',
  tag: '[LIVE]', tagColor: '#39ff14',
  desc: 'Refresh a key and poll for recent presence. Absence may mean a stopped writer, a partition, or a missed update.',
  code: `loop {\n  PUT /v1/data/svc:api:heartbeat X-TTL:300\n  sleep 60s\n}`
},
{
  name: 'state machine',
  tag: '[FSM]', tagColor: '#22d3ee',
  desc: 'A job-id key transitions through states via overwrites. TTL guarantees staleness: if the writer crashes, the key expires.',
  code: `PUT job:42  queued       X-TTL:600\nPUT job:42  in_progress  X-TTL:600\nPUT job:42  complete     X-TTL:600`
},
{
  name: 'presence broadcast',
  tag: '[NET]', tagColor: '#e040fb',
  desc: 'Participants write presence keys. Readers poll a prefix and inspect the live values visible on their node.',
  code: `PUT  /v1/data/room:42:alice\nGET  /v1/keys?prefix=room:42:\n   → ["alice","bob","charlotte"]`
}];


function Patterns() {
  return (
    <TerminalFrame id="patterns" label="PATTERNS://AGENT" style={{ marginTop: 44 }}>
      <h2 style={{
        color: '#39ff14', fontSize: 22, margin: '0 0 6px',
        letterSpacing: '0.05em', textShadow: '0 0 10px #39ff1480'
      }}>six ways agents use temporary state</h2>
      <p style={{ color: '#8b949e', fontSize: 13, margin: '0 0 22px' }}>
        the primitive is general-purpose. the primitive is also <code style={{
          background: '#161b22', color: '#22d3ee', padding: '0.15em 0.4em',
          borderRadius: 3, fontSize: '0.9em'
        }}>pipe</code>, not <code style={{
          background: '#161b22', color: '#22d3ee', padding: '0.15em 0.4em',
          borderRadius: 3, fontSize: '0.9em'
        }}>grep</code>.
      </p>
      <div style={{
        display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(320px, 1fr))',
        gap: 18
      }}>
        {PATTERNS.map((p) => <PatternCard key={p.name} {...p} />)}
      </div>
    </TerminalFrame>);

}

function PatternCard({ name, tag, tagColor, desc, code }) {
  const [hover, setHover] = useStateR(false);
  return (
    <article
      onMouseEnter={() => setHover(true)}
      onMouseLeave={() => setHover(false)}
      style={{
        border: `1px solid ${hover ? '#39ff1480' : '#22d3ee33'}`,
        boxShadow: hover ? '0 0 8px #39ff1430, inset 0 0 8px #39ff1410' : 'none',
        padding: '18px 20px 16px',
        transition: 'all 0.2s',
        background: '#0a0a0fcc'
      }}>
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'baseline', marginBottom: 10 }}>
        <h3 style={{
          color: '#39ff14', fontSize: 16, margin: 0, fontWeight: 700,
          letterSpacing: '0.02em',
          textShadow: hover ? '0 0 8px #39ff1480' : 'none'
        }}>{name}</h3>
        <span style={{
          color: tagColor, fontSize: 10, letterSpacing: '0.14em',
          fontWeight: 700
        }}>{tag}</span>
      </div>
      <p style={{ color: '#b8c0cc', fontSize: 13, lineHeight: 1.6, margin: '0 0 12px' }}>{desc}</p>
      <pre style={{
        background: '#161b22', border: '1px solid #22d3ee33', borderRadius: 4,
        color: '#22d3ee', fontSize: 11.5, lineHeight: 1.55,
        padding: '10px 12px', margin: 0, overflow: 'auto',
        fontFamily: 'var(--font-mono)'
      }}>{code}</pre>
    </article>);

}

// =============================================================================
// Interactive TTL playground
// =============================================================================

function TTLDemo() {
  const [entries, setEntries] = useStateR([]);
  const [now, setNow] = useStateR(Date.now());
  const [key, setKey] = useStateR('mykey');
  const [val, setVal] = useStateR('hello');
  const [ttl, setTtl] = useStateR(10);

  useEffectR(() => {
    const t = setInterval(() => setNow(Date.now()), 250);
    return () => clearInterval(t);
  }, []);

  const live = useMemoR(() =>
  entries.filter((e) => e.expiresAt > now), [entries, now]);

  const store = () => {
    if (!key.trim()) return;
    setEntries((prev) => [
    ...prev.filter((e) => e.key !== key),
    { key, val, createdAt: Date.now(), expiresAt: Date.now() + ttl * 1000 }]
    );
  };

  return (
    <TerminalFrame id="demo" label="DEMO://EPHEMERAL" style={{ marginTop: 44 }}>
      <h2 style={{
        color: '#39ff14', fontSize: 22, margin: '0 0 6px',
        letterSpacing: '0.05em', textShadow: '0 0 10px #39ff1480'
      }}>watch data expire</h2>
      <p style={{ color: '#8b949e', fontSize: 13, margin: '0 0 22px' }}>
        write a key. set a TTL. watch it vanish. this is simulated client-side; the real thing does this across a gossip cluster.
      </p>

      <div style={{
        display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 22
      }}>
        <div>
          <FieldLabel>key</FieldLabel>
          <TermInput value={key} onChange={setKey} placeholder="mykey" />
          <FieldLabel>value</FieldLabel>
          <TermInput value={val} onChange={setVal} placeholder="hello" />
          <FieldLabel>ttl (seconds)</FieldLabel>
          <div style={{ display: 'flex', alignItems: 'center', gap: 12 }}>
            <input type="range" min="3" max="60" step="1"
            value={ttl} onChange={(e) => setTtl(+e.target.value)}
            style={{ flex: 1, accentColor: '#39ff14' }} />
            <span style={{ color: '#39ff14', fontSize: 14, minWidth: 42, textAlign: 'right' }}>
              {ttl}s
            </span>
          </div>
          <button onClick={store} style={{
            marginTop: 18, padding: '10px 16px',
            background: 'transparent', border: '1px solid #39ff14',
            color: '#39ff14', fontFamily: 'var(--font-mono)',
            fontSize: 13, letterSpacing: '0.08em', cursor: 'pointer',
            transition: 'all 0.2s'
          }}
          onMouseEnter={(e) => {
            e.currentTarget.style.background = '#39ff14';
            e.currentTarget.style.color = '#0a0a0f';
            e.currentTarget.style.boxShadow = '0 0 12px #39ff1460';
          }}
          onMouseLeave={(e) => {
            e.currentTarget.style.background = 'transparent';
            e.currentTarget.style.color = '#39ff14';
            e.currentTarget.style.boxShadow = 'none';
          }}>
            &gt; PUT /v1/data/{key || '…'}</button>

          <pre style={{
            marginTop: 18, background: '#161b22', border: '1px solid #22d3ee33',
            borderRadius: 4, color: '#22d3ee', fontSize: 11.5, lineHeight: 1.55,
            padding: '10px 12px', fontFamily: 'var(--font-mono)', overflow: 'auto'
          }}>{`curl -X PUT -H "X-TTL: ${ttl}" \\\n  -d "${val}" \\\n  http://localhost:8080/v1/data/${encodeURIComponent(key)}`}</pre>
        </div>

        <div>
          <FieldLabel>network state</FieldLabel>
          <div style={{
            background: '#0d1117', border: '1px solid #22d3ee33',
            padding: '14px 14px 8px', minHeight: 240,
            fontFamily: 'var(--font-mono)', fontSize: 12.5
          }}>
            {live.length === 0 &&
            <div style={{ color: '#8b949e' }}>
                <span style={{ color: '#22d3ee' }}>guest@network</span>
                <span>:</span>
                <span style={{ color: '#39ff14' }}>~</span>
                <span>$ </span>
                <span>// the network is empty. it forgot everything.</span>
              </div>
            }
            {live.map((e) => {
              const remainingMs = Math.max(0, e.expiresAt - now);
              const pct = Math.max(0, Math.min(1, remainingMs / (e.expiresAt - e.createdAt)));
              const danger = remainingMs < 3000;
              return (
                <div key={e.key} style={{ marginBottom: 12 }}>
                  <div style={{ display: 'flex', justifyContent: 'space-between', marginBottom: 4 }}>
                    <span>
                      <span style={{ color: '#22d3ee' }}>{e.key}</span>
                      <span style={{ color: '#8b949e' }}> = </span>
                      <span style={{ color: '#b8c0cc' }}>"{e.val}"</span>
                    </span>
                    <span style={{
                      color: danger ? '#e040fb' : '#39ff14',
                      textShadow: danger ? '0 0 8px #e040fb80' : 'none',
                      fontVariantNumeric: 'tabular-nums'
                    }}>
                      {(remainingMs / 1000).toFixed(1)}s
                    </span>
                  </div>
                  <div style={{
                    height: 3, background: '#22d3ee22', position: 'relative',
                    overflow: 'hidden'
                  }}>
                    <div style={{
                      position: 'absolute', top: 0, left: 0, bottom: 0,
                      width: `${pct * 100}%`,
                      background: danger ? '#e040fb' : '#39ff14',
                      boxShadow: danger ? '0 0 6px #e040fb80' : '0 0 6px #39ff1460',
                      transition: 'width 0.25s linear, background 0.2s'
                    }}></div>
                  </div>
                </div>);

            })}
          </div>

          <div style={{ marginTop: 14, color: '#8b949e', fontSize: 11.5, lineHeight: 1.6 }}>
            <span style={{ color: '#facc15' }}>[SYS]</span> expired keys are
            deleted on every node. the network keeps no memory of what it forgot.
          </div>
        </div>
      </div>
    </TerminalFrame>);

}

function FieldLabel({ children }) {
  return <div style={{
    color: '#22d3ee', fontSize: 11, letterSpacing: '0.12em',
    textTransform: 'uppercase', margin: '12px 0 6px'
  }}>// {children}</div>;
}

function TermInput({ value, onChange, placeholder }) {
  return (
    <input type="text" value={value} placeholder={placeholder}
    onChange={(e) => onChange(e.target.value)}
    style={{
      width: '100%', background: '#0d1117',
      border: '1px solid #22d3ee4d', color: '#b8c0cc',
      padding: '9px 12px', fontFamily: 'var(--font-mono)',
      fontSize: 13, outline: 'none', caretColor: '#39ff14',
      transition: 'border-color 0.2s, box-shadow 0.2s'
    }}
    onFocus={(e) => {
      e.target.style.borderColor = '#39ff1480';
      e.target.style.boxShadow = '0 0 8px #39ff1430, inset 0 0 6px #39ff1410';
    }}
    onBlur={(e) => {
      e.target.style.borderColor = '#22d3ee4d';
      e.target.style.boxShadow = 'none';
    }} />);


}

// =============================================================================
// Quickstart — tabbed code blocks
// =============================================================================

const QUICKSTART_TABS = [
{
  id: 'node', label: 'node', glyph: '>',
  code: `# from the source checkout
go build -o bin/server ./cmd/server
NODE_NETWORK=private NODE_MAX_STORAGE_MB=50 ./bin/server

# or build a private cluster with two enclaves
docker compose up --build
# enclave-a: localhost:8091, :8092
# enclave-b: localhost:8093`
},
{
  id: 'mcp', label: 'mcp', glyph: '@',
  code: `{
  "mcpServers": {
    "sopholeth": {
      "command": "/absolute/path/to/checkout/bin/server",
      "args": ["--mcp"]
    }
  }
}`
},
{
  id: 'curl', label: 'raw api', glyph: '$',
  code: `# store with 5-minute TTL
curl -X PUT -H "X-TTL: 300" --data-binary "hello" http://localhost:8080/v1/data/myapp:message
# 201: quorum observed; 202: stored locally, unconfirmed

# retrieve from this node
curl http://localhost:8080/v1/data/myapp:message

# list live keys
curl "http://localhost:8080/v1/keys?prefix=myapp:&limit=100"`
},
{
  id: 'js', label: 'javascript', glyph: '#',
  code: `const url = 'http://localhost:8080/v1/data/task:42';
const stored = await fetch(url, {
  method: 'PUT',
  headers: { 'X-TTL': '300' },
  body: JSON.stringify({ handoff: true })
});
if (!stored.ok) throw new Error('Write failed');

const response = await fetch(url);
if (response.ok) console.log(await response.json());
else if (response.status === 404) console.log('No live value here');
else throw new Error('Read failed');`
}];

function Quickstart() {
  const [tab, setTab] = useStateR(QUICKSTART_TABS[0].id);
  const [copied, setCopied] = useStateR(false);
  const current = QUICKSTART_TABS.find((t) => t.id === tab);

  const copy = () => {
    navigator.clipboard.writeText(current.code).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <TerminalFrame id="quickstart" label="GETTING://STARTED" style={{ marginTop: 44 }}>
      <h2 style={{
        color: '#39ff14', fontSize: 22, margin: '0 0 6px',
        letterSpacing: '0.05em', textShadow: '0 0 10px #39ff1480'
      }}>quickstart</h2>
      <p style={{ color: '#8b949e', fontSize: 13, margin: '0 0 18px' }}>
        build from source, then try a private node through HTTP or MCP.
        {tab === 'mcp' && <><br />use the absolute binary path in your MCP client configuration.
          tools: store, retrieve, exists, list_keys. the embedded node also opens an HTTP listener.</>}
      </p>

      <div style={{
        display: 'flex', gap: 2, borderBottom: '1px solid #22d3ee33',
        marginBottom: 0, flexWrap: 'wrap'
      }}>
        {QUICKSTART_TABS.map((t) =>
        <button key={t.id} onClick={() => setTab(t.id)}
        style={{
          background: tab === t.id ? '#161b22' : 'transparent',
          border: 'none',
          borderTop: `1px solid ${tab === t.id ? '#22d3ee99' : 'transparent'}`,
          borderLeft: `1px solid ${tab === t.id ? '#22d3ee33' : 'transparent'}`,
          borderRight: `1px solid ${tab === t.id ? '#22d3ee33' : 'transparent'}`,
          borderBottom: `1px solid ${tab === t.id ? '#161b22' : 'transparent'}`,
          marginBottom: -1,
          color: tab === t.id ? '#39ff14' : '#8b949e',
          textShadow: tab === t.id ? '0 0 8px #39ff1460' : 'none',
          padding: '10px 16px', fontSize: 13,
          letterSpacing: '0.08em', fontFamily: 'var(--font-mono)',
          cursor: 'pointer',
          transition: 'all 0.2s'
        }}>
            <span style={{ color: '#22d3ee', marginRight: 6 }}>{t.glyph}</span>{t.label}
          </button>
        )}
      </div>

      <div style={{ position: 'relative' }}>
        <button onClick={copy} style={{
          position: 'absolute', top: 12, right: 12,
          background: copied ? '#39ff14' : 'transparent',
          color: copied ? '#0a0a0f' : '#22d3ee',
          border: `1px solid ${copied ? '#39ff14' : '#22d3ee99'}`,
          padding: '4px 10px', fontSize: 10.5, letterSpacing: '0.14em',
          fontFamily: 'var(--font-mono)', cursor: 'pointer',
          transition: 'all 0.2s', zIndex: 1
        }}>{copied ? 'COPIED' : 'COPY'}</button>
        <pre style={{
          background: '#161b22',
          border: '1px solid #22d3ee33',
          borderTop: 'none',
          color: '#b8c0cc', fontSize: 13, lineHeight: 1.65,
          padding: '22px 20px', margin: 0,
          fontFamily: 'var(--font-mono)', overflow: 'auto',
          borderRadius: '0 0 4px 4px',
          minHeight: 200
        }}>{current.code}</pre>
      </div>
    </TerminalFrame>);

}

// =============================================================================
// API reference — compact endpoint table
// =============================================================================

const ENDPOINTS = [
{ method: 'PUT', path: '/v1/data/{key}', desc: 'store bytes under key with X-TTL header',
  returns: '201 Created · 202 Accepted', color: '#39ff14' },
{ method: 'GET', path: '/v1/data/{key}', desc: 'retrieve bytes (or 404 if expired)',
  returns: '200 · 404', color: '#22d3ee' },
{ method: 'HEAD', path: '/v1/data/{key}', desc: 'existence check without body — for heartbeats',
  returns: '200 · 404', color: '#22d3ee' },
{ method: 'GET', path: '/v1/keys', desc: 'list keys · supports ?prefix, ?limit, ?cursor',
  returns: '200 · {keys, next_cursor?}', color: '#22d3ee' },
{ method: 'GET', path: '/v1/health', desc: 'node liveness',
  returns: '200 {status, node_id, network}', color: '#8b949e' },
{ method: 'GET', path: '/v1/status', desc: 'detailed status — uptime, memory',
  returns: '200 {…}', color: '#8b949e' },
{ method: 'GET', path: '/v1/topology', desc: 'peer list with enclave membership',
  returns: '200 {peers:[…]}', color: '#8b949e' },
{ method: 'GET', path: '/v1/metrics', desc: 'prometheus-format metrics',
  returns: '200 text/plain', color: '#8b949e' }];


function ApiReference() {
  return (
    <TerminalFrame id="api" label="API://v1" style={{ marginTop: 44 }}>
      <h2 style={{
        color: '#39ff14', fontSize: 22, margin: '0 0 6px',
        letterSpacing: '0.05em', textShadow: '0 0 10px #39ff1480'
      }}>API reference</h2>
      <p style={{ color: '#8b949e', fontSize: 13, margin: '0 0 18px' }}>
        client and diagnostic endpoints. no client accounts. bytes in, bytes out.
      </p>

      <div style={{
        fontFamily: 'var(--font-mono)', fontSize: 13,
        background: '#0d1117', border: '1px solid #22d3ee33'
      }}>
        <div style={{
          display: 'grid',
          gridTemplateColumns: '80px 1fr 1fr 180px',
          padding: '10px 16px',
          background: '#161b22',
          borderBottom: '1px solid #22d3ee33',
          color: '#22d3ee', fontSize: 10.5, letterSpacing: '0.14em',
          textTransform: 'uppercase'
        }}>
          <span>method</span><span>path</span><span>description</span><span>returns</span>
        </div>
        {ENDPOINTS.map((e, i) =>
        <div key={i} style={{
          display: 'grid',
          gridTemplateColumns: '80px 1fr 1fr 180px',
          padding: '11px 16px',
          borderBottom: i < ENDPOINTS.length - 1 ? '1px solid #22d3ee1a' : 'none',
          alignItems: 'baseline'
        }}>
            <span style={{
            color: e.color, fontWeight: 700, letterSpacing: '0.04em'
          }}>{e.method}</span>
            <span style={{ color: '#b8c0cc' }}>{e.path}</span>
            <span style={{ color: '#8b949e', fontSize: 12 }}>{e.desc}</span>
            <span style={{ color: '#8b949e', fontSize: 11.5 }}>{e.returns}</span>
          </div>
        )}
      </div>
    </TerminalFrame>);

}

// =============================================================================
// Footer — neofetch-style
// =============================================================================

const NEOFETCH_ASCII = String.raw`
       :::::::::::
     :::::::::::::        guest@network
    :::::::::::::.        -------------
    ::::::::::::::         Project: Sopholeth
     ::::::::::::.         Kernel:  gossip/TTL
      ::::::::::           Uptime:  ephemeral
       ::::::::.           Shell:   dead-drop
         ::::::            Memory:  configurable · TTL-bounded
`;

function Footer() {
  return (
    <footer style={{
      marginTop: 80, padding: '36px 0 24px',
      borderTop: '1px solid #22d3ee33',
      fontSize: 12, lineHeight: 1.7
    }}>
      <div style={{ display: 'grid', gridTemplateColumns: 'minmax(0,1fr) minmax(0,1fr)', gap: 28 }}>
        <pre style={{
          color: '#39ff14', textShadow: '0 0 6px #39ff1440',
          fontSize: 'clamp(8px, 0.95vw, 11.5px)', lineHeight: 1.2,
          background: 'transparent', border: 'none', padding: 0, margin: 0,
          fontFamily: 'var(--font-mono)', overflow: 'hidden'
        }}>{NEOFETCH_ASCII}</pre>

        <div>
          <div style={{ color: '#22d3ee', fontSize: 11, letterSpacing: '0.14em', marginBottom: 12 }}>
            // LINKS://EXTERNAL
          </div>
          <div style={{ display: 'grid', gridTemplateColumns: '1fr 1fr', gap: 8 }}>
            <FooterLink glyph="@" label="github / source"
            href={PROJECT_REPOSITORY} />
            <FooterLink glyph=">" label="run locally" href="#quickstart" />
            <FooterLink glyph="#" label="architecture"
            href={`${PROJECT_REPOSITORY}/blob/main/docs/architecture.md`} />
            <FooterLink glyph="#" label="core principles"
            href={`${PROJECT_REPOSITORY}/blob/main/docs/core-principles.md`} />
            <FooterLink glyph="#" label="usage patterns"
            href={`${PROJECT_REPOSITORY}/blob/main/docs/patterns.md`} />
          </div>
        </div>
      </div>

      <div style={{
        marginTop: 28, paddingTop: 18, borderTop: '1px solid #22d3ee1a',
        display: 'flex', justifyContent: 'space-between', flexWrap: 'wrap', gap: 12,
        color: '#8b949e', fontSize: 11, letterSpacing: '0.05em'
      }}>
        <span>Sopholeth · wisdom through intentional forgetting</span>
        <span>
          built by <a href="https://www.clocktowerassoc.com" style={{ color: '#22d3ee' }}>
            clocktower & associates</a> · <a href="https://www.wshoffner.dev" style={{ color: '#22d3ee' }}>
            wshoffner.dev</a>
        </span>
      </div>
      <div style={{ marginTop: 8, color: '#8b949e', fontSize: 11, fontStyle: 'italic' }}>
        // this footer will not self-destruct. the data it describes will.
      </div>
    </footer>);

}

function FooterLink({ glyph, label, href }) {
  return (
    <a href={href} style={{
      color: '#8b949e', fontSize: 12, padding: '2px 0',
      transition: 'color 0.2s, text-shadow 0.2s'
    }}
    onMouseEnter={(e) => {
      e.currentTarget.style.color = '#39ff14';
      e.currentTarget.style.textShadow = '0 0 8px #39ff1460';
    }}
    onMouseLeave={(e) => {
      e.currentTarget.style.color = '#8b949e';
      e.currentTarget.style.textShadow = 'none';
    }}>
      
      <span style={{ color: '#22d3ee', marginRight: 8 }}>{glyph}</span>{label}
    </a>);

}

Object.assign(window, {
  SiteNav, Hero, WhatItIs, Architecture, Patterns,
  TTLDemo, Quickstart, ApiReference, Footer
});
