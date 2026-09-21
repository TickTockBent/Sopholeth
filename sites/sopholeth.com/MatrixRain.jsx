// MatrixRain.jsx — landing-page canvas rain at 6% alpha, disabled under reduced-motion and mobile.
const { useEffect, useRef } = React;

function MatrixRain() {
  const ref = useRef(null);
  useEffect(() => {
    const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
    const mobile = window.matchMedia('(max-width: 640px)').matches;
    if (reduced || mobile) return;
    const canvas = ref.current;
    const ctx = canvas.getContext('2d');
    let w = 0, h = 0, cols = [], raf = 0, last = 0;
    const glyphs = 'アァカサタナハマヤャラワガザダバパイィキシチニヒミリヰギジヂビピウヴクスツヌフムユュルグズヅブプエェケセテネヘメレヱゲゼデベペオォコソトノホモヨョロヲゴゾドボポヴッン01'.split('');
    function resize(){
      w = canvas.width = canvas.offsetWidth;
      h = canvas.height = canvas.offsetHeight;
      const size = 14;
      cols = new Array(Math.floor(w / size)).fill(0).map(() => Math.random() * h / size);
      ctx.font = `${size}px "JetBrains Mono", monospace`;
    }
    function frame(t){
      if (t - last > 50) {
        last = t;
        ctx.fillStyle = 'rgba(10,10,15,0.08)';
        ctx.fillRect(0,0,w,h);
        ctx.fillStyle = '#39ff14';
        const size = 14;
        for (let i=0;i<cols.length;i++){
          const ch = glyphs[(Math.random()*glyphs.length)|0];
          ctx.fillText(ch, i*size, cols[i]*size);
          if (cols[i]*size > h && Math.random() > 0.975) cols[i] = 0;
          cols[i]++;
        }
      }
      raf = requestAnimationFrame(frame);
    }
    resize();
    const onR = () => resize();
    window.addEventListener('resize', onR);
    raf = requestAnimationFrame(frame);
    return () => { cancelAnimationFrame(raf); window.removeEventListener('resize', onR); };
  }, []);
  return <canvas ref={ref} style={{
    position:'absolute', inset:0, width:'100%', height:'100%',
    opacity:0.06, pointerEvents:'none'
  }} />;
}

Object.assign(window, { MatrixRain });
