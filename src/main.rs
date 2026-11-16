mod git;
mod tui;

use ratatui::crossterm::event::{self, Event, KeyEventKind};

const USAGE: &str = "usage: git edit [repo-path]\n\nEdit Git commit metadata (authors, dates, messages) in a terminal UI.\nOpens the current repository when no path is given.";

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if args.iter().any(|a| a == "-h" || a == "--help") {
        println!("{USAGE}");
        return;
    }
    let path = match tui::initial_repository_path(&args, std::env::current_dir) {
        Ok(p) => p,
        Err(e) => {
            eprintln!("{e}");
            std::process::exit(1);
        }
    };

    let mut terminal = ratatui::init();
    let result = run(&mut terminal, tui::Model::new(&path));
    ratatui::restore();
    if let Err(e) = result {
        eprintln!("git-edit failed: {e}");
        std::process::exit(1);
    }
}

fn run(terminal: &mut ratatui::DefaultTerminal, mut model: tui::Model) -> std::io::Result<()> {
    while !model.quit {
        terminal.draw(|f| model.view(f))?;
        // Draw the busy state first, then do the blocking git work.
        if let Some(pending) = model.pending.take() {
            model.run_pending(pending);
            continue;
        }
        if let Event::Key(key) = event::read()?
            && key.kind == KeyEventKind::Press
        {
            model.handle_key(key);
        }
    }
    Ok(())
}
