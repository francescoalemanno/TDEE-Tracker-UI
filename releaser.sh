#!/bin/bash

# Script per automatizzare il processo di release di un progetto Golang
# Esegue tagging di una nuova release e cross-compilazione per Windows, Linux e macOS

set -e  # Interrompe l'esecuzione se un comando fallisce

# Colori per output
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
NC='\033[0m' # No Color

# Nome del progetto (modificare secondo necessità)
PROJECT_NAME=$(basename $(pwd))
# Directory per i binari compilati
BUILD_DIR="./bin"

# Funzione di aiuto
show_help() {
    echo -e "${YELLOW}Utilizzo:${NC} $0 [opzioni]"
    echo
    echo -e "${YELLOW}Opzioni:${NC}"
    echo "  -v, --version VERSION    Specifica la versione per il tag (es. v1.0.0)"
    echo "  -n, --name NAME          Specifica il nome del progetto (default: nome della directory corrente)"
    echo "  -h, --help               Mostra questo messaggio di aiuto"
    echo
    echo -e "${YELLOW}Esempio:${NC}"
    echo "  $0 --version v1.2.3 --name mio-progetto"
    exit 0
}

# Verifica dipendenze
check_dependencies() {
    echo -e "${YELLOW}Verifico dipendenze...${NC}"
    
    if ! command -v git &> /dev/null; then
        echo -e "${RED}Git non trovato. Installare git e riprovare.${NC}"
        exit 1
    fi
    
    if ! command -v go &> /dev/null; then
        echo -e "${RED}Go non trovato. Installare Go e riprovare.${NC}"
        exit 1
    fi
    
    # Verifica che ci troviamo in un repository git
    if [ ! -d .git ]; then
        echo -e "${RED}Directory .git non trovata. Eseguire lo script all'interno di un repository git.${NC}"
        exit 1
    fi
    
    # Verifica che ci siano file .go nel progetto
    if [ -z "$(find . -name "*.go" -type f -not -path "./vendor/*" -not -path "./.git/*")" ]; then
        echo -e "${RED}Nessun file Go trovato nel progetto.${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}Tutte le dipendenze sono soddisfatte.${NC}"
}

# Parsing degli argomenti da linea di comando
parse_arguments() {
    while [[ $# -gt 0 ]]; do
        case $1 in
            -v|--version)
                VERSION="$2"
                shift 2
                ;;
            -n|--name)
                PROJECT_NAME="$2"
                shift 2
                ;;
            -h|--help)
                show_help
                ;;
            *)
                echo -e "${RED}Opzione non riconosciuta: $1${NC}"
                show_help
                ;;
        esac
    done
    
    # Verifica che la versione sia stata specificata
    if [ -z "$VERSION" ]; then
        echo -e "${RED}Errore: È necessario specificare una versione con l'opzione -v o --version${NC}"
        show_help
    fi
    
    # Verifica che il formato della versione sia corretto (vX.Y.Z)
    if ! [[ $VERSION =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
        echo -e "${RED}Errore: Il formato della versione deve essere vX.Y.Z (es. v1.0.0)${NC}"
        exit 1
    fi
}

# Verifica lo stato del repository
check_repo_status() {
    echo -e "${YELLOW}Verifico lo stato del repository...${NC}"
    
    # Controlla se ci sono modifiche non committate
    if ! git diff-index --quiet HEAD --; then
        echo -e "${RED}Ci sono modifiche non committate nel repository.${NC}"
        echo -e "${YELLOW}Esegui un commit delle modifiche prima di procedere con il rilascio.${NC}"
        exit 1
    fi
    
    # Controlla se il tag esiste già
    if git tag | grep -q "^$VERSION$"; then
        echo -e "${RED}Il tag $VERSION esiste già.${NC}"
        echo -e "${YELLOW}Scegli una versione diversa o elimina il tag esistente con 'git tag -d $VERSION'.${NC}"
        exit 1
    fi
    
    echo -e "${GREEN}Repository in uno stato valido per il rilascio.${NC}"
}

# Crea un nuovo tag per la release
create_release_tag() {
    echo -e "${YELLOW}Creazione del tag per la release $VERSION...${NC}"
    
    # Crea un tag annotato
    git tag -a "$VERSION" -m "Release $VERSION"
    
    # Chiedi all'utente se desidera fare push del tag
    read -p "Vuoi fare push del tag $VERSION su GitHub? (s/n): " PUSH_TAG
    if [[ $PUSH_TAG =~ ^[Ss]$ ]]; then
        echo -e "${YELLOW}Eseguo push del tag su GitHub...${NC}"
        git push origin "$VERSION"
        echo -e "${GREEN}Tag $VERSION pushato con successo!${NC}"
    else
        echo -e "${YELLOW}Il tag $VERSION è stato creato localmente.${NC}"
        echo -e "${YELLOW}Esegui 'git push origin $VERSION' per pusharlo su GitHub.${NC}"
    fi
}

# Build dei binari per diverse piattaforme
build_binaries() {
    echo -e "${YELLOW}Compilazione dei binari per la release $VERSION...${NC}"
    
    # Crea directory di build se non esiste
    mkdir -p "$BUILD_DIR"
    
    # Array delle piattaforme target
    PLATFORMS=("windows/amd64" "windows/386" "linux/amd64" "linux/386" "linux/arm" "linux/arm64" "darwin/amd64" "darwin/arm64")
    
    # Compila per ogni piattaforma
    for PLATFORM in "${PLATFORMS[@]}"; do
        # Estrai OS e ARCH
        OS=$(echo $PLATFORM | cut -d'/' -f1)
        ARCH=$(echo $PLATFORM | cut -d'/' -f2)
        
        # Definisci nome del file di output
        if [ "$OS" = "windows" ]; then
            OUTPUT="$BUILD_DIR/${PROJECT_NAME}_${VERSION}_${OS}_${ARCH}.exe"
        else
            OUTPUT="$BUILD_DIR/${PROJECT_NAME}_${VERSION}_${OS}_${ARCH}"
        fi
        
        echo -e "${YELLOW}Compilazione per $OS/$ARCH...${NC}"
        
        # Compila
        GOOS=$OS GOARCH=$ARCH go build -o "$OUTPUT" -ldflags "-X main.Version=$VERSION" ./...
        
        # Verifica se la compilazione è avvenuta con successo
        if [ $? -eq 0 ]; then
            echo -e "${GREEN}Compilazione per $OS/$ARCH completata: $OUTPUT${NC}"
        else
            echo -e "${RED}Errore durante la compilazione per $OS/$ARCH${NC}"
        fi
    done
}

# Crea archivi compressi dei binari
create_archives() {
    echo -e "${YELLOW}Creazione degli archivi compressi...${NC}"
    
    # Verifica che la directory di build esista
    if [ ! -d "$BUILD_DIR" ]; then
        echo -e "${RED}Directory di build non trovata.${NC}"
        return
    fi
    
    # Crea directory per gli archivi
    ARCHIVE_DIR="$BUILD_DIR/archives"
    mkdir -p "$ARCHIVE_DIR"
    
    # Crea archivi per ogni binario
    for BINARY in $(find "$BUILD_DIR" -type f -not -path "$ARCHIVE_DIR/*"); do
        # Salta se non è un file eseguibile
        if [ ! -x "$BINARY" ]; then
            continue
        fi
        
        BINARY_NAME=$(basename "$BINARY")
        
        if [[ "$BINARY_NAME" == *.exe ]]; then
            # Per Windows, crea zip
            ZIP_NAME="${BINARY_NAME%.exe}.zip"
            echo -e "${YELLOW}Creazione archivio: $ZIP_NAME${NC}"
            
            if command -v zip &> /dev/null; then
                (zip -j "$ARCHIVE_DIR/$ZIP_NAME" "$BUILD_DIR/$BINARY_NAME")
                echo -e "${GREEN}Archivio creato: $ARCHIVE_DIR/$ZIP_NAME${NC}"
            else
                echo -e "${RED}Il comando 'zip' non è disponibile. Installare zip per creare archivi per Windows.${NC}"
            fi
        else
            # Per Linux e macOS, crea tar.gz
            TAR_NAME="${BINARY_NAME}.tar.gz"
            echo -e "${YELLOW}Creazione archivio: $TAR_NAME${NC}"
            
            (tar -cvzf "$ARCHIVE_DIR/$TAR_NAME" -C "$BUILD_DIR" "$BINARY_NAME")
            echo -e "${GREEN}Archivio creato: $ARCHIVE_DIR/$TAR_NAME${NC}"
        fi
    done
}

# Visualizza istruzioni per creare una release su GitHub
show_github_instructions() {
    echo -e "\n${YELLOW}=== Prossimi passi per completare la release su GitHub ===${NC}"
    echo -e "1. Vai alla pagina GitHub del tuo repository"
    echo -e "2. Clicca su 'Releases'"
    echo -e "3. Clicca su 'Draft a new release'"
    echo -e "4. Seleziona il tag '$VERSION'"
    echo -e "5. Aggiungi un titolo e descrizione per la release"
    echo -e "6. Carica i binari compilati dalla directory: $BUILD_DIR"
    echo -e "7. Clicca su 'Publish release'"
    echo -e "\n${GREEN}I binari compilati sono disponibili in: $BUILD_DIR${NC}"
    echo -e "${GREEN}Gli archivi compressi sono disponibili in: $BUILD_DIR/archives${NC}"
}

# Funzione principale
main() {
    echo -e "${GREEN}=== Script di automazione per release di progetti Golang ===${NC}"
    
    # Verifica dipendenze
    check_dependencies
    
    # Parsing degli argomenti
    parse_arguments "$@"
    
    # Controlla lo stato del repository
    check_repo_status
    
    # Crea il tag per la release
    create_release_tag
    
    # Compila i binari
    build_binaries
    
    # Crea archivi compressi
    create_archives
    
    # Mostra istruzioni per GitHub
    show_github_instructions
    
    echo -e "\n${GREEN}=== Processo di release completato con successo! ===${NC}"
}

# Esegui la funzione principale passando tutti gli argomenti
main "$@"