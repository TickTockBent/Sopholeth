// Landing page interactive effects
document.addEventListener('DOMContentLoaded', function() {

    // Add glitch effect to title on hover
    const title = document.querySelector('h1');
    if (title) {
        title.addEventListener('mouseenter', function() {
            this.style.animation = 'glitch 0.3s infinite';
        });

        title.addEventListener('mouseleave', function() {
            this.style.animation = 'glitch 2s infinite';
        });
    }

    // Add copy to clipboard functionality for code blocks
    const codeBlocks = document.querySelectorAll('.code-block pre code');
    codeBlocks.forEach(codeBlock => {
        const pre = codeBlock.parentElement;
        const copyBtn = document.createElement('button');
        copyBtn.textContent = 'COPY';
        copyBtn.className = 'copy-btn';
        copyBtn.style.cssText = `
            position: absolute;
            top: 10px;
            right: 10px;
            background: #000;
            border: 1px solid #00ff00;
            color: #00ff00;
            padding: 5px 10px;
            font-size: 0.7rem;
            cursor: pointer;
            font-family: 'Share Tech Mono', monospace;
            text-transform: uppercase;
            letter-spacing: 0.1em;
            transition: all 0.3s ease;
        `;

        copyBtn.addEventListener('mouseenter', function() {
            this.style.background = '#00ff00';
            this.style.color = '#000';
            this.style.boxShadow = '0 0 10px #00ff00';
        });

        copyBtn.addEventListener('mouseleave', function() {
            this.style.background = '#000';
            this.style.color = '#00ff00';
            this.style.boxShadow = 'none';
        });

        copyBtn.addEventListener('click', function() {
            navigator.clipboard.writeText(codeBlock.textContent).then(() => {
                this.textContent = 'COPIED!';
                this.style.color = '#ffff00';
                this.style.borderColor = '#ffff00';
                setTimeout(() => {
                    this.textContent = 'COPY';
                    this.style.color = '#00ff00';
                    this.style.borderColor = '#00ff00';
                }, 2000);
            });
        });

        pre.style.position = 'relative';
        pre.appendChild(copyBtn);
    });

    // Add animated typing effect to hero description
    const heroDesc = document.querySelector('.hero-description p');
    if (heroDesc) {
        const originalHTML = heroDesc.innerHTML;
        heroDesc.innerHTML = '';

        // Extract text content for typing, then restore HTML
        let charIndex = 0;
        const textContent = heroDesc.textContent || '';

        function typeWriter() {
            if (charIndex < originalHTML.length) {
                heroDesc.innerHTML = originalHTML.substring(0, charIndex + 1);
                charIndex++;
                // Skip ahead through HTML tags
                while (charIndex < originalHTML.length && originalHTML[charIndex - 1] === '<') {
                    const closeIndex = originalHTML.indexOf('>', charIndex);
                    if (closeIndex !== -1) {
                        charIndex = closeIndex + 1;
                        heroDesc.innerHTML = originalHTML.substring(0, charIndex);
                    } else {
                        break;
                    }
                }
                setTimeout(typeWriter, 20);
            }
        }

        setTimeout(typeWriter, 1000);
    }

    // Add hover effects to feature cards
    const featureCards = document.querySelectorAll('.feature-card');
    featureCards.forEach(card => {
        card.addEventListener('mouseenter', function() {
            const icon = this.querySelector('.feature-icon');
            if (icon) {
                icon.style.transform = 'scale(1.2) rotate(10deg)';
                icon.style.transition = 'all 0.3s ease';
            }
        });

        card.addEventListener('mouseleave', function() {
            const icon = this.querySelector('.feature-icon');
            if (icon) {
                icon.style.transform = 'scale(1) rotate(0deg)';
            }
        });
    });

    // Matrix rain effect (subtle)
    function createMatrixRain() {
        const canvas = document.createElement('canvas');
        canvas.style.cssText = `
            position: fixed;
            top: 0;
            left: 0;
            width: 100vw;
            height: 100vh;
            pointer-events: none;
            z-index: 1;
            opacity: 0.1;
        `;
        document.body.appendChild(canvas);

        const ctx = canvas.getContext('2d');
        canvas.width = window.innerWidth;
        canvas.height = window.innerHeight;

        const chars = '0101';
        const fontSize = 14;
        const columns = canvas.width / fontSize;
        const drops = [];

        for (let i = 0; i < columns; i++) {
            drops[i] = 1;
        }

        function draw() {
            ctx.fillStyle = 'rgba(0, 0, 0, 0.05)';
            ctx.fillRect(0, 0, canvas.width, canvas.height);

            ctx.fillStyle = '#00ff00';
            ctx.font = fontSize + 'px monospace';

            for (let i = 0; i < drops.length; i++) {
                const text = chars[Math.floor(Math.random() * chars.length)];
                ctx.fillText(text, i * fontSize, drops[i] * fontSize);

                if (drops[i] * fontSize > canvas.height && Math.random() > 0.975) {
                    drops[i] = 0;
                }

                drops[i]++;
            }
        }

        setInterval(draw, 100);

        window.addEventListener('resize', function() {
            canvas.width = window.innerWidth;
            canvas.height = window.innerHeight;
        });
    }

    setTimeout(createMatrixRain, 2000);

    // Scroll-triggered animations
    const observerOptions = {
        threshold: 0.1,
        rootMargin: '0px 0px -50px 0px'
    };

    const observer = new IntersectionObserver(function(entries) {
        entries.forEach(entry => {
            if (entry.isIntersecting) {
                entry.target.style.opacity = '1';
                entry.target.style.transform = 'translateY(0)';
            }
        });
    }, observerOptions);

    const sections = document.querySelectorAll('.features-grid, .demo-section, .architecture-section, .getting-started, .use-cases');
    sections.forEach(section => {
        section.style.opacity = '0';
        section.style.transform = 'translateY(30px)';
        section.style.transition = 'all 0.6s ease';
        observer.observe(section);
    });

    // Sound effects on demo buttons
    let audioContext;

    function createBeep(frequency = 800, duration = 100) {
        if (!audioContext) {
            audioContext = new (window.AudioContext || window.webkitAudioContext)();
        }

        const oscillator = audioContext.createOscillator();
        const gainNode = audioContext.createGain();

        oscillator.connect(gainNode);
        gainNode.connect(audioContext.destination);

        oscillator.frequency.value = frequency;
        oscillator.type = 'square';

        gainNode.gain.setValueAtTime(0.1, audioContext.currentTime);
        gainNode.gain.exponentialRampToValueAtTime(0.001, audioContext.currentTime + duration / 1000);

        oscillator.start(audioContext.currentTime);
        oscillator.stop(audioContext.currentTime + duration / 1000);
    }

    const demoButtons = document.querySelectorAll('.demo-btn');
    demoButtons.forEach(btn => {
        btn.addEventListener('mouseenter', () => {
            try {
                createBeep(600, 50);
            } catch (e) {
                // Audio not supported or blocked
            }
        });
    });

    console.log(`
    ██████╗ ███████╗██████╗ ██████╗  █████╗ ███╗   ███╗
    ██╔══██╗██╔════╝██╔══██╗██╔══██╗██╔══██╗████╗ ████║
    ██████╔╝█████╗  ██████╔╝██████╔╝███████║██╔████╔██║
    ██╔══██╗██╔══╝  ██╔═══╝ ██╔══██╗██╔══██║██║╚██╔╝██║
    ██║  ██║███████╗██║     ██║  ██║██║  ██║██║ ╚═╝ ██║
    ╚═╝  ╚═╝╚══════╝╚═╝     ╚═╝  ╚═╝╚═╝  ╚═╝╚═╝     ╚═╝

    Sopholeth - Wisdom through intentional forgetting
    `);
});
