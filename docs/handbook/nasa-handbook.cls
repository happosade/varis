\NeedsTeXFormat{LaTeX2e}
\ProvidesClass{nasa-handbook}[2026/11/02 Apollo Operations Handbook systems data page]

\newif\ifnh@letter
\DeclareOption{letter}{\nh@lettertrue}
\newif\ifnh@handbook
\DeclareOption{handbook}{\nh@handbooktrue}
\ProcessOptions\relax
\LoadClass{article}

\RequirePackage{iftex}
\RequireXeTeX
\RequirePackage{fontspec}
\RequirePackage{tikz}
\usetikzlibrary{arrows.meta,patterns.meta,svg.path}
\RequirePackage{tabularx}

\ifnh@letter
  \def\nh@paperw{215.9}\def\nh@paperh{279.4}
  \RequirePackage[paperwidth=215.9mm,paperheight=279.4mm,left=15.5mm,right=20.4mm,top=24mm,bottom=21.2mm]{geometry}
\else
  \def\nh@paperw{210}\def\nh@paperh{297}
  \RequirePackage[paperwidth=210mm,paperheight=297mm,left=15.5mm,right=14.5mm,top=24mm,bottom=21.2mm]{geometry}
\fi
\pagestyle{empty}
\raggedbottom
\raggedright
\setlength\parindent{9mm}
\hyphenpenalty=10000 \exhyphenpenalty=10000
\setlength\parskip{0pt}
\setlength\emergencystretch{2em}

% ---------- fonts (the fonts/ folder sits next to the page) ----------
\IfFontExistsTF{Iowan Old Style}
  {\setmainfont{Iowan Old Style}[LetterSpace=5.5]}
  {\setmainfont{texgyrepagella}[Extension=.otf,UprightFont=*-regular,BoldFont=*-bold,
    ItalicFont=*-italic,BoldItalicFont=*-bolditalic,LetterSpace=5.5]}
\newfontfamily\nh@leroy{routed-gothic-narrow.ttf}[Path=fonts/]
\newfontfamily\nh@leroywide{routed-gothic.ttf}[Path=fonts/]
\newfontfamily\nh@selectric{LetterGothicCode-R.ttf}[Path=fonts/,BoldFont=LetterGothicCode-B.ttf]
\IfFontExistsTF{Helvetica Neue}
  {\newfontfamily\nh@flag{Helvetica Neue}}
  {\newfontfamily\nh@flag{texgyreheros-regular.otf}}

\renewcommand\normalsize{\fontsize{8pt}{10.64pt}\selectfont}
\normalsize

% Leroy's "1" reads as "I"; it and the glyphs Routed Gothic lacks come from \nh@flag.
\newXeTeXintercharclass\nh@fbclass
\XeTeXcharclass"0031=\nh@fbclass
\XeTeXcharclass"2264=\nh@fbclass
\XeTeXcharclass"2265=\nh@fbclass
\XeTeXcharclass"2248=\nh@fbclass
\XeTeXcharclass"00AB=\nh@fbclass
\XeTeXcharclass"00BB=\nh@fbclass
\XeTeXinterchartoks 0 \nh@fbclass={\begingroup\nh@flag}
\XeTeXinterchartoks 4095 \nh@fbclass={\begingroup\nh@flag}
\XeTeXinterchartoks \nh@fbclass 0={\endgroup}
\XeTeXinterchartoks \nh@fbclass 4095={\endgroup}

\newcommand\nh@leroyat[2]{#1\fontsize{#2}{1.2\dimexpr#2\relax}\selectfont\XeTeXinterchartokenstate=1\relax}
\newcommand\nhlbl{\nh@leroyat\nh@leroy{2.35mm}}
\newcommand\nhlbls{\nh@leroyat\nh@leroy{2.05mm}}
\newcommand\nhlblb{\nh@leroyat\nh@leroywide{2.5mm}}
\newcommand\nhcode{\nh@leroyat\nh@leroywide{2.4mm}}
\newcommand\nhtag{\nh@leroyat\nh@leroy{1.8mm}}
\newcommand\nhspace[1]{\addfontfeatures{LetterSpace=#1}}
\newcommand\nowrap[1]{\mbox{#1}}

% ---------- page metadata ----------
\pgfkeys{/nh/.is family,/nh,
  doc/.store in=\nh@doc, title/.store in=\nh@title, part/.store in=\nh@part,
  section/.store in=\nh@section, subsection/.store in=\nh@subsection,
  subsystem/.store in=\nh@subsystem, tab/.store in=\nh@tab, tab top/.store in=\nh@tabtop,
  mission/.store in=\nh@mission, basic date/.store in=\nh@basicdate, change date/.store in=\nh@changedate,
  issuer/.store in=\nh@issuer,
  doc=, title=, part=SYSTEMS DATA, section=2, subsection=2.1, subsystem=, tab=,
  tab top=112, mission=, basic date=, change date=, issuer=}
\newcommand\nhsetup[1]{\pgfkeys{/nh,#1}}

% ---------- running head, footer and thumb tab: drawn on every page ----------
\newcommand\nh@ul[1]{\leavevmode\setbox0\hbox{#1}\rlap{\rule[-1.6pt]{\wd0}{0.5pt}}\box0}
\newcommand\nh@fill[2]{\leavevmode\rlap{\rule[-1.3mm]{#1}{0.6pt}}\makebox[#1]{\fontsize{8.8pt}{10pt}\selectfont\nhspace{3}#2}}
\def\nh@pageno{\nh@subsection-\thepage}
\newif\ifnh@bare
\AddToHook{shipout/background}{\ifnh@bare\global\nh@barefalse\else\put(0,0){\begin{tikzpicture}[overlay,x=1mm,y=1mm]
  \pgfmathsetmacro\nh@cx{15.5+180/2}
  \node[anchor=base,inner sep=0,align=center,font=\fontsize{8.6pt}{10.75pt}\selectfont\bfseries\nhspace{7}] at (\nh@cx,-15.5) {\nh@doc\\\nh@title};
  \node[anchor=base,inner sep=0,font=\fontsize{8.2pt}{10pt}\selectfont\nhspace{10}] at (\nh@cx,-21.6) {\nh@part};
  \fill (13.5,-23.9) rectangle (196.5,-23.9-0.53);
  \fill (13.5,-\nh@paperh+20.6) rectangle (196.5,-\nh@paperh+20.6-0.53);
  \node[anchor=base,inner sep=0,font=\fontsize{8.2pt}{10pt}\selectfont\nhspace{10}] at (\nh@cx,-\nh@paperh+15.7) {\nh@subsystem};
  \node[anchor=base west,inner sep=0,font=\fontsize{8.2pt}{10pt}\selectfont] at (15.5,-\nh@paperh+9.4)
    {\makebox[180mm]{Mission \nh@fill{20mm}{\nh@mission}\hfill Basic Date \nh@fill{30mm}{\nh@basicdate}\hfill
      Change Date \nh@fill{30mm}{\nh@changedate}\hfill Page \nh@fill{18mm}{\nh@pageno}}};
  \ifx\nh@tab\@empty\else
    \fill (\nh@paperw-8.5,-\nh@tabtop) rectangle (\nh@paperw,-\nh@tabtop-17);
    \node[white,inner sep=0,font=\nh@leroywide\fontsize{8pt}{8pt}\selectfont\nhspace{8}] at (\nh@paperw-4.25,-\nh@tabtop-8.5) {\nh@tab};
  \fi
\end{tikzpicture}}\fi}

% ---------- section opener ----------
\newcommand\nhopener{\par\vspace*{-1.2mm}
  {\centering\nhspace{12}\baselineskip=12.8pt SECTION \nh@section\hspace{1.2em}·\hspace{1.2em}SUBSECTION \nh@subsection\\\nh@subsystem\par}%
  \vspace{2mm}}

% ---------- numbered paragraphs ----------
% \begin{nhpara}[change bar height]{2.1.1}{INTRODUCTION.}
\newcommand\nh@bar[1]{\ifx\relax#1\relax\else\llap{\smash{\rule[\dimexpr2.6mm-#1\relax]{1.3mm}{#1}}\hspace{7.7mm}}\fi}
\NewDocumentEnvironment{nh@para}{m m m m}{%
  \begin{list}{}{\setlength\leftmargin{19mm}\setlength\labelwidth{19mm}\setlength\labelsep{0pt}%
    \setlength\itemindent{0pt}\setlength\listparindent{9mm}\setlength\parsep{1mm}%
    \setlength\topsep{0pt}\setlength\partopsep{0pt}\setlength\itemsep{0pt}}%
  \item[\nh@bar{#2}{\nhspace{8}#3}\hfill]#1{#4}\par\vspace{-0.2mm}}
  {\end{list}}
\NewDocumentEnvironment{nhpara}{O{} m m}
  {\nh@tocpara{#2}{#3}\begin{nh@para}{\nhspace{8}}{#1}{#2}{#3}}{\end{nh@para}}
\NewDocumentEnvironment{nhsubpara}{O{} m m}
  {\begin{nh@para}{\nhspace{6}\nh@ul}{#1}{#2}{#3}\@afterindentfalse\@afterheading}{\end{nh@para}}

% ---------- figures: 1 unit = 1 mm, origin top left, y down, 180 mm wide ----------
% \begin{nhfigure}[top y]{height mm}{2.1-1}{Caption}{TAB-501A}
\NewDocumentEnvironment{nhfigure}{O{0} m m m m}
  {\par\vspace{2.6mm}\noindent\begin{tikzpicture}[nh]
    \useasboundingbox (0,#1) rectangle (180,#1+#2);
    \node[code,r] at (178.6,#1+#2-0.6) {#5};}
  {\end{tikzpicture}\par\nointerlineskip\vspace{0.6mm}%
    {\centering\nhspace{7}Figure #3.\hspace{3mm}#4\par}\nh@lof{#3}{#4}}

\tikzset{
  nh/.style={x=1mm,y=-1mm,line cap=butt,every node/.style={inner sep=0pt,outer sep=0pt,anchor=base west,font=\nhlbl}},
  o/.style={draw,line width=0.42mm,line join=round,line cap=round},
  of/.style={o,fill=white},
  t/.style={draw,line width=0.17mm,line cap=round},
  hid/.style={draw,line width=0.22mm,dash pattern=on 1.3mm off 0.7mm},
  cl/.style={draw,line width=0.16mm,dash pattern=on 5mm off 0.8mm on 0.9mm off 0.8mm},
  cut/.style={draw,line width=0.5mm,dash pattern=on 5mm off 1mm on 1mm off 1mm},
  ld/.style={draw,line width=0.16mm,-{Triangle[length=2.1mm,width=1.05mm]}},
  dim/.style={draw,line width=0.16mm,{Triangle[length=2.2mm,width=0.95mm]}-},
  w/.style={draw,line width=0.3mm,line join=round},
  wa/.style={w,-{Triangle[length=2.4mm,width=1.3mm]}},
  box/.style={draw,fill=white,line width=0.45mm},
  panel/.style={draw,line width=0.3mm,dash pattern=on 2mm off 1mm},
  ev/.style={draw,line width=0.18mm,dash pattern=on 1mm off 0.7mm},
  vid/.style={draw,line width=0.35mm,pattern={Lines[angle=45,distance=1.1mm,line width=0.2mm]}},
  hatch/.style={pattern={Lines[angle=45,distance=1.1mm,line width=0.2mm]}},
  aud/.style={draw,fill=white,line width=0.35mm},
  gap/.style={aud,dash pattern=on 0.6mm off 0.5mm},
  grain/.style={draw,line width=0.35mm,pattern={Dots[radius=0.15mm,distance=0.92mm,angle=45]}},
  bp/.style={draw,line width=0.35mm,pattern={Lines[angle=-45,distance=1.4mm,line width=0.7mm]}},
  dot/.style={fill},
  dep/.style={draw,line width=0.2mm,dash pattern=on 1.3mm off 0.7mm,-{Straight Barb[length=1.9mm,width=2.2mm]}},
  gen/.style={draw,line width=0.3mm,-{Triangle[open,length=2.6mm,width=2.4mm,fill=white]}},
  state/.style={box,rounded corners=1.8mm},
  act/.style={draw,fill=white,line width=0.3mm},
  lbl/.style={font=\nhlbl},
  lbl-s/.style={font=\nhlbls},
  lbl-b/.style={font=\nhlblb},
  code/.style={font=\nhcode},
  tag/.style={font=\nhtag},
  c/.style={anchor=base},
  r/.style={anchor=base east},
  from svg/.style={xscale=2.84528,yscale=-2.84528},
  up/.style={rotate=90},
}

% ---------- lower band: table left, notice and checklist right ----------
\NewDocumentEnvironment{nhlower}{}
  {\par\vspace{2.8mm}\noindent\begin{minipage}[t]{\dimexpr\linewidth-70mm}\raggedright}
  {\end{minipage}}
\newcommand\nhrightcolumn{\end{minipage}\hspace{6mm}\begin{minipage}[t]{64mm}\raggedright}

\newcommand\nh@tablefont{\fontsize{7.3pt}{8.76pt}\selectfont\setlength\arrayrulewidth{0.6pt}%
  \setlength\tabcolsep{1.6mm}\renewcommand\arraystretch{1.23}%
  \renewcommand\tabularxcolumn[1]{>{\raggedright\arraybackslash}p{##1}}\noindent}
\newcommand\nh@tabcap[2]{{\centering\nhspace{7}Table #1. #2\par}\nh@lot{#1}{#2}\vspace{0.8mm}\nh@tablefont}
% \begin{nhgrid}{4-1}{Title}{|l|X|c|}{Head&Head&Head} rows: a&b&c\\\hline
\NewDocumentEnvironment{nhgrid}{m m m m}
  {\nh@tabcap{#1}{#2}\tabularx{\linewidth}{#3}\hline#4\\\hline}
  {\endtabularx}
\NewDocumentEnvironment{nhtable}{m m}
  {\nh@tabcap{#1}{#2}\tabularx{\linewidth}{|X|l|}\hline
    \multicolumn{1}{|c|}{\nhspace{10}Parameter}&\multicolumn{1}{c|}{\nhspace{10}Value}\\\hline}
  {\endtabularx}
\newcommand\nhrow[2]{#1&#2\\\hline}

\newcommand\nhnotice[2]{{\centering\bfseries\nhspace{18}\nh@ul{#1}\par}\vspace{0.8mm}%
  {\fontsize{7.4pt}{9.47pt}\selectfont#2\par}\vspace{1.8mm}}

\newsavebox\nh@card
\NewDocumentEnvironment{nhchecklist}{m m}
  {\begin{lrbox}{\nh@card}\begin{minipage}{\dimexpr64mm-1.6pt}%
    \nh@selectric\fontsize{7.1pt}{9.23pt}\selectfont\setlength\parindent{0pt}%
    {\setlength\fboxsep{0pt}\colorbox{black}{\parbox[c][4.3mm][c]{\linewidth}{%
      \hspace*{1.8mm}\textcolor{white}{\bfseries#1\hfill#2}\hspace*{1.8mm}}}}\par
    \vspace{0.8mm}\leftskip=1.8mm\rightskip=1.8mm}
  {\par\vspace{1mm}\end{minipage}\end{lrbox}%
    {\setlength\fboxsep{0pt}\setlength\fboxrule{0.8pt}\noindent\fbox{\usebox\nh@card}}\par}
% sidebar box; \nhbox content stays on one page
\NewDocumentEnvironment{nhbox}{m}
  {\nh@lob{#1}\begin{lrbox}{\nh@card}\begin{minipage}{\dimexpr\linewidth-4mm-1.6pt}%
    {\centering\bfseries\nhspace{12}#1\par}\vspace{1.2mm}\fontsize{7.4pt}{9.47pt}\selectfont\setlength\parindent{0pt}}
  {\par\end{minipage}\end{lrbox}%
    {\setlength\fboxsep{2mm}\setlength\fboxrule{0.8pt}\noindent\fbox{\usebox\nh@card}}\par}
% decision record: \begin{nhdecision}{D-1}{TITLE}{ACCEPTED, 1 JANUARY 1970} \nhfield{CONTEXT}{…} …
\NewDocumentEnvironment{nhdecision}{m m m}
  {\begin{nhbox}{DECISION #1.\hspace{2mm}#2}\nhfield{STATUS}{#3}}
  {\end{nhbox}}
\newcommand\nhfield[2]{\par\vspace{0.9mm}\noindent{\nhspace{10}#1.}\hspace{2mm}#2\par}

\newcommand\nhstep[2]{\noindent\makebox[3.2mm][l]{#1}#2\par}
\newcommand\nhverify[1]{\noindent\hspace*{3.2mm}\hspace*{2ex}√ #1\par}

% ---------- fit check: room left above the footer, in the log ----------
\newcommand\nh@tocpara[2]{}
\newcommand\nh@lof[2]{}
\newcommand\nh@lot[2]{}
\newcommand\nh@lob[1]{}

% ---------- handbook mode: front matter, chapters, appendices, contents ----------
\ifnh@handbook
\def\nh@pageno{\thepage}
\renewcommand\thepage{\roman{page}}
\def\nh@sectionword{SECTION}
\newcounter{nhchapter}
\renewcommand\nh@tocpara[2]{\addcontentsline{toc}{nhpara}{\protect\numberline{#1}#2}}
\renewcommand\nh@lof[2]{\addcontentsline{lof}{nhfig}{\protect\numberline{#1}#2}}
\renewcommand\nh@lot[2]{\addcontentsline{lot}{nhfig}{\protect\numberline{#1}#2}}
\renewcommand\nh@lob[1]{\addcontentsline{lob}{nhbox}{#1}}
\renewcommand\@pnumwidth{3.2em}
\renewcommand\@tocrmarg{4.2em}
\newcommand\l@nhchapter[2]{\vspace{2.2mm}\@dottedtocline{0}{0mm}{9mm}{#1}{#2}}
\newcommand\l@nhpara{\@dottedtocline{1}{9mm}{14mm}}
\newcommand\l@nhfig{\@dottedtocline{1}{0mm}{16mm}}
\newcommand\l@nhbox{\@dottedtocline{1}{0mm}{0mm}}
\newcommand\nh@heading[1]{{\centering\nhspace{12}#1\par}\vspace{4mm}}

\newcommand\nhcover{\global\nh@baretrue\null\vspace*{55mm}%
  {\centering\bfseries\nhspace{10}\fontsize{11pt}{15pt}\selectfont\nh@doc\par\vspace{6mm}
    \rule{120mm}{0.53mm}\par\vspace{7mm}
    \fontsize{15pt}{21pt}\selectfont\nh@title\par\vspace{6mm}
    \mdseries\fontsize{10pt}{14pt}\selectfont\nh@part\par\vspace{7mm}
    \rule{120mm}{0.53mm}\par}
  \vfill{\centering\nhspace{10}\fontsize{8.6pt}{12pt}\selectfont\nh@issuer\par\vspace{2mm}\nh@basicdate\par}%
  \vspace*{12mm}\clearpage}

\NewDocumentEnvironment{nhhistory}{}
  {\clearpage\nh@heading{RECORD OF REVISIONS/CHANGES}\nh@tablefont
    \tabularx{\linewidth}{|c|c|X|l|}\hline
    \nhspace{10}Rev Ltr&\nhspace{10}Chg No.&\multicolumn{1}{c|}{\nhspace{10}Description}&\nhspace{10}Date\\\hline}
  {\endtabularx}
\newcommand\nhhist[4]{#1&#2&#3&#4\\\hline}

\newcommand\nh@list[2]{\par\vspace{6mm}\nh@heading{#1}\@starttoc{#2}}
\newcommand\nhcontents{\clearpage\nh@heading{TABLE OF CONTENTS}\@starttoc{toc}%
  \clearpage\nh@list{LIST OF FIGURES}{lof}\nh@list{LIST OF TABLES}{lot}\nh@list{LIST OF BOXES}{lob}}

% \nhchapter[TAB]{TITLE}; \nhchapter*{PREFACE} is unnumbered and keeps the page numbering
\NewDocumentCommand\nhchapter{s O{} m}{\clearpage\def\nh@tab{#2}\def\nh@subsystem{#3}%
  \IfBooleanTF{#1}
    {\addcontentsline{toc}{nhchapter}{#3}\nh@heading{#3}}
    {\stepcounter{nhchapter}\edef\nh@section{\thenhchapter}%
      \setcounter{page}{1}\renewcommand\thepage{\nh@section-\arabic{page}}%
      \addcontentsline{toc}{nhchapter}{\protect\numberline{\nh@section}#3}%
      \vspace*{-1.2mm}{\centering\nhspace{12}\baselineskip=12.8pt\nh@sectionword\ \nh@section\\\relax#3\par}\vspace{2mm}}}
\AddToHook{env/nhpara/before}{\par\vspace{1.6mm}}
\AddToHook{env/nhfigure/after}{\vspace{2.2mm}}
\AddToHook{env/nhtable/before}{\par\vspace{2.6mm}}
\AddToHook{env/nhgrid/before}{\par\vspace{2.6mm}}
\AddToHook{env/nhgrid/after}{\vspace{1.4mm}}
\AddToHook{env/nhtable/after}{\vspace{1.4mm}}
\AddToHook{env/nhbox/before}{\par\vspace{2.6mm}}
\AddToHook{env/nhchecklist/before}{\par\vspace{1mm}}
\AddToHook{cmd/nhnotice/before}{\par\vspace{2.6mm}}
\newcommand\nhappendix{\setcounter{nhchapter}{0}\renewcommand\thenhchapter{\Alph{nhchapter}}\def\nh@sectionword{APPENDIX}}
\fi

\AtEndDocument{\ifnh@handbook\typeout{NH-FIT: handbook}\else\par\edef\nh@free{\fpeval{round((\pagegoal-\pagetotal)/(1mm),1)}}%
  \typeout{NH-FIT: \ifdim\nh@free pt<0pt OVERFLOW by \fpeval{-\nh@free} mm\else\nh@free\space mm free above the footer\fi}\fi}
